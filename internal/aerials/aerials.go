// Package aerials turns macOS's aerial video downloads off and on.
// idleassetsd downloads videos only for the categories, subcategories
// and videos picked for a user in its database, Aerial.sqlite, and keeps
// downloading after a still wallpaper replaces an aerial, since the
// picks stay. Off clears the picks as root and stops the running
// downloader, which keeps the picks it read when it started; on puts the
// recorded picks back. Measured on macOS 15.7 only (ARCHITECTURE.md
// decisions log, 2026-10-23); on a later release both change nothing.
package aerials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
)

const (
	// DB is idleassetsd's database through macOS 15. It is readable by
	// every user and writable only by root.
	DB = "/Library/Application Support/com.apple.idleassetsd/Aerial.sqlite"
	// Daemon is the downloader's process name.
	Daemon = "idleassetsd"
	// OSMax is the last release the picks were measured on, in the
	// rules' os_max terms: macOS 26 follows 15.
	OSMax = "25"
	// RuleID is the rule whose videos the picks keep downloading; the
	// journal files off and on under it.
	RuleID = "aerial-wallpapers"
)

// Kinds of the journal lines off and on write.
const (
	KindOff = "aerials-off"
	KindOn  = "aerials-on"
)

// table is one of the join tables idleassetsd downloads for: what it
// picks, and the columns holding the picked item and the user, both by
// primary key. The names are macOS 15.7's.
type table struct {
	name, itemCol, userCol, itemTable string
}

var tables = []table{
	{"Z_2ACTIVEFORUSERS", "Z_2ACTIVECATEGORIES", "Z_4ACTIVEFORUSERS", "ZCATEGORY"},
	{"Z_3ACTIVEFORUSERS", "Z_3ACTIVESUBCATEGORIES", "Z_4ACTIVEFORUSERS1", "ZSUBCATEGORY"},
	{"Z_1PINNEDFORUSERS", "Z_1PINNEDASSETS", "Z_4PINNEDFORUSERS", "ZASSET"},
}

func tableNamed(name string) (table, bool) {
	for _, t := range tables {
		if t.name == name {
			return t, true
		}
	}
	return table{}, false
}

// Pick is one picked category, subcategory or video, and the user it is
// picked for, by primary key and by identifier. Off deletes the row by
// its keys; on inserts it again by its identifiers, which survive the
// database numbering its rows anew.
type Pick struct {
	Table  string `json:"table"`
	Item   int64  `json:"item"`
	User   int64  `json:"user"`
	ItemID string `json:"item_id"`
	UserID string `json:"user_id"`
}

// identifier is what an identifier must look like to be written into
// the SQL run as root: the database holds UUIDs and user ids, and the
// record that carries them back to on is a file the user can edit.
var identifier = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Counts is how many picks of each kind.
type Counts struct {
	Categories    int `json:"categories"`
	Subcategories int `json:"subcategories"`
	Videos        int `json:"videos"`
}

// Count tallies picks by table.
func Count(picks []Pick) Counts {
	var c Counts
	for _, p := range picks {
		switch p.Table {
		case tables[0].name:
			c.Categories++
		case tables[1].name:
			c.Subcategories++
		case tables[2].name:
			c.Videos++
		}
	}
	return c
}

// Text is the counts in words, leaving out the zeros.
func (c Counts) Text() string {
	var parts []string
	for _, n := range []struct {
		n             int
		one, multiple string
	}{
		{c.Categories, "category", "categories"},
		{c.Subcategories, "subcategory", "subcategories"},
		{c.Videos, "video", "videos"},
	} {
		switch {
		case n.n == 1:
			parts = append(parts, "1 "+n.one)
		case n.n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n.n, n.multiple))
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// State is what the picks say about the downloads.
type State string

const (
	StateOn         State = "on"         // something is picked: macOS downloads for it
	StateOff        State = "off"        // nothing is picked
	StateAbsent     State = "absent"     // no database: aerials were never set up here
	StateUnknown    State = "unknown"    // the database could not be read
	StateUnmeasured State = "unmeasured" // a macOS release the picks were not measured on
)

// Record is what the last off cleared, kept for on.
type Record struct {
	Cleared time.Time `json:"cleared"`
	Picks   []Pick    `json:"picks"`
	// Restored is when on put the picks back; a restored record has
	// nothing left to put back.
	Restored *time.Time `json:"restored,omitempty"`
}

// Waiting reports whether the record holds picks on can put back.
func (r *Record) Waiting() bool {
	return r != nil && r.Restored == nil && len(r.Picks) > 0
}

// Status is the picks as read, without root.
type Status struct {
	State   State  `json:"state"`
	Version string `json:"version,omitempty"`
	Picks   []Pick `json:"picks,omitempty"`
	Counts  Counts `json:"counts"`
	// Detail says why the state is unknown or unmeasured.
	Detail string `json:"detail,omitempty"`
	// Recorded is the last off's record, nil when there is none.
	Recorded *Record `json:"recorded,omitempty"`
	// RecordError says why the record could not be read.
	RecordError string `json:"record_error,omitempty"`
	// SetFor names the surfaces the wallpaper store sets an aerial for
	// (SurfaceWallpaper, SurfaceScreenSaver). Clearing the picks leaves
	// such a surface no videos to play, and macOS may pick its aerial
	// again.
	SetFor []string `json:"set_for,omitempty"`
	// SetForError says why the wallpaper store could not be read.
	SetForError string `json:"set_for_error,omitempty"`
}

// Aerials reads and changes the picks of one database.
type Aerials struct {
	// DB is the database; empty means DB.
	DB string
	// Version is the macOS release, as engine.Host carries it.
	Version string
	// RecordPath is the file the last off's record is kept in.
	RecordPath string
	// Store is the user's wallpaper store (WallpaperStorePath); empty
	// reads none.
	Store string
	// Log journals off and on before they act (invariant 6).
	Log interface{ Append(oplog.Entry) error }
	// Authorize asks for the password before off or on reads the picks
	// it acts on, as `sudo -v` does in a terminal, so a pick changed
	// while the prompt is open is read. Nil asks nothing first: Root's
	// command asks itself, after the picks were read.
	Authorize func(ctx context.Context) error
	// Root turns a shell script into the command that runs it as root,
	// such as `sudo -n` once Authorize has the password. The journal
	// records that command.
	Root func(script string) []string
	// Run runs a command. Off and on read the root step's exit status
	// from its error (ExitCode, as *exec.ExitError has it); a runner
	// that did not start the step returns ErrNotRun. Nil runs it through
	// RunRoot with the terminal's stdio.
	Run func(ctx context.Context, argv []string) error
	// Daemon is the downloader's process name; empty means Daemon.
	// Tests name a process of their own.
	Daemon string
	// Now is injectable for tests.
	Now func() time.Time
}

// RecordPath is where regrow keeps the last off's record.
func RecordPath() (string, error) {
	dir, err := oplog.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aerials.json"), nil
}

func (a *Aerials) db() string {
	if a.DB == "" {
		return DB
	}
	return a.DB
}

func (a *Aerials) daemon() string {
	if a.Daemon == "" {
		return Daemon
	}
	return a.Daemon
}

func (a *Aerials) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *Aerials) run(ctx context.Context, argv []string) error {
	if a.Run != nil {
		return a.Run(ctx, argv)
	}
	return RunRoot(ctx, argv, os.Stdout, os.Stderr)
}

// RunRoot runs a command with stdin from the terminal. A stop sends
// SIGTERM, then SIGKILL after 5 s: at its password prompt sudo puts the
// terminal's echo back on SIGTERM, which a killed sudo does not. sudo
// passes SIGTERM on to the shell it runs, not to the shell's children
// (sudo 1.9.13p2, macOS 15.7). A command that could not start returns
// ErrNotRun.
func RunRoot(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRun, err)
	}
	err := cmd.Wait()
	if err != nil && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// Once Cancel ran, Wait reports the stop even for an exit 0.
		return nil
	}
	return err
}

// Status reads the picks and the record. It needs no root and changes
// nothing.
func (a *Aerials) Status(ctx context.Context) Status {
	st := Status{Version: a.Version}
	rec, err := a.readRecord()
	if err != nil {
		st.RecordError = err.Error()
	}
	st.Recorded = rec
	switch {
	case a.Version == "":
		st.State, st.Detail = StateUnmeasured, "regrow could not read the macOS version"
		return st
	case engine.CompareVersion(a.Version, OSMax) > 0:
		st.State, st.Detail = StateUnmeasured, "the aerial picks were measured on macOS 15 only; on macOS "+a.Version+" regrow changes nothing"
		return st
	}
	if a.Store != "" {
		surfaces, err := aerialSurfaces(ctx, a.Store)
		if err != nil {
			st.SetForError = err.Error()
		}
		st.SetFor = surfaces
	}
	if _, err := os.Stat(a.db()); errors.Is(err, fs.ErrNotExist) {
		st.State = StateAbsent
		return st
	}
	picks, err := readPicks(ctx, a.db())
	if err != nil {
		st.State, st.Detail = StateUnknown, err.Error()
		return st
	}
	st.Picks, st.Counts = picks, Count(picks)
	st.State = StateOff
	if len(picks) > 0 {
		st.State = StateOn
	}
	return st
}

// readPicks reads every pick, read-only, through the system's sqlite3.
func readPicks(ctx context.Context, db string) ([]Pick, error) {
	var sql strings.Builder
	for _, t := range tables {
		fmt.Fprintf(&sql, "SELECT '%s', j.%s, j.%s, IFNULL(i.ZIDENTIFIER, ''), IFNULL(u.ZIDENTIFIER, '') FROM %s j LEFT JOIN %s i ON i.Z_PK = j.%s LEFT JOIN ZUSER u ON u.Z_PK = j.%s;\n",
			t.name, t.itemCol, t.userCol, t.name, t.itemTable, t.itemCol, t.userCol)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/sqlite3", "-readonly", "-batch", "-bail", "-noheader", "-list", "-separator", "|", db, sql.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("read the aerial picks: %s", msg)
		}
		return nil, fmt.Errorf("read the aerial picks: %w", err)
	}
	var picks []Pick
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		p, err := parsePick(line)
		if err != nil {
			return nil, err
		}
		picks = append(picks, p)
	}
	return picks, nil
}

func parsePick(line string) (Pick, error) {
	f := strings.Split(line, "|")
	if len(f) != 5 {
		return Pick{}, fmt.Errorf("read the aerial picks: unexpected row %q", line)
	}
	item, err1 := strconv.ParseInt(f[1], 10, 64)
	user, err2 := strconv.ParseInt(f[2], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return Pick{}, fmt.Errorf("read the aerial picks: unexpected row %q", line)
	}
	return Pick{Table: f[0], Item: item, User: user, ItemID: f[3], UserID: f[4]}, nil
}

func (a *Aerials) readRecord() (*Record, error) {
	data, err := os.ReadFile(a.RecordPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", a.RecordPath, err)
	}
	return &r, nil
}

// writeRecord replaces the record in one rename, synced first: off
// writes it before it acts, and a crash must leave the old record or
// the new one, never half of one.
func (a *Aerials) writeRecord(r Record) error {
	if a.RecordPath == "" {
		return errors.New("no place to keep the record: regrow's state folder is unknown")
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.RecordPath), 0o700); err != nil {
		return err
	}
	tmp := a.RecordPath + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, a.RecordPath)
}

// Result is what off or on did. After is read again once the root
// step's command has run, whatever its status; otherwise it is Before.
type Result struct {
	// Ran is true when the root step may have changed the picks: it ran,
	// and its status said neither that it changed nothing (exit 1) nor
	// that it never started (ErrNotRun). It is false when there was
	// nothing to do, a refusal, or a stop before the step. After says
	// where the picks stand.
	Ran bool `json:"ran"`
	// Run names the journal run; empty when nothing was journaled.
	Run string `json:"run,omitempty"`
	// Before and After are the picks as read around the root step,
	// Before once the password is given.
	Before Status `json:"before"`
	After  Status `json:"after"`
	// Missing counts recorded picks on could not put back, their
	// category, subcategory, video or user no longer in the database.
	Missing int `json:"missing,omitempty"`
}

// ErrNotApplicable is returned by off and on where they change
// nothing: a release the picks were not measured on, no database, or a
// database that could not be read. Status.Detail says which.
var ErrNotApplicable = errors.New("aerials: cannot change the picks here")

// ErrStopped is returned when off or on was stopped while its root
// step ran, and the step's exit status does not say what it did:
// stopping ends the command that runs it, which may leave the root
// shell running, so the step may still finish.
var ErrStopped = errors.New("stopped before the root step ended; it may still finish")

// ErrNotRun is a root step that never started, so nothing changed: a
// runner returns it for a command that could not start, or a password
// prompt cancelled before the step.
var ErrNotRun = errors.New("the root step did not run; nothing changed")

// errNotStarted is a stop that came before the root step ran.
var errNotStarted = errors.New("stopped before the root step started; nothing changed")

// ErrBusy is returned by off and on while another off or on holds the
// lock beside the record, in this process or another, and, as a
// heldError, while the root step of one stopped earlier may still run.
var ErrBusy = errors.New("another `regrow aerials off` or `on` is running; nothing changed")

// heldError is ErrBusy for a stopped root step that may still run.
type heldError struct{ until time.Time }

func (e heldError) Error() string {
	return "the root step of an off or on stopped earlier may still be running; nothing changed, try again after " + e.until.Local().Format(time.TimeOnly)
}

func (e heldError) Is(target error) bool { return target == ErrBusy }

// The root scripts' exit statuses, which decide the record.
const (
	exitDone    = 0  // the step did all it was asked
	exitNothing = 1  // nothing changed: the one transaction rolled back, or sudo refused the step
	exitLived   = 2  // off cleared the picks, but a downloader outlived SIGKILL
	exitUnknown = -1 // no status: a signal, or a runner that cannot say
)

// stepLife bounds a root step from its start: sqlite3 waits at most 5 s
// for the database, then a downloader gets 5 s after SIGTERM and 2 s
// after SIGKILL.
const stepLife = 15 * time.Second

// exitStatus is the root step's exit status from its runner's error.
func exitStatus(err error) int {
	var exit interface{ ExitCode() int }
	switch {
	case err == nil:
		return exitDone
	case errors.Is(err, ErrNotRun), errors.Is(err, errNotStarted):
		return exitNothing
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		return exit.ExitCode()
	}
	return exitUnknown
}

// begin reads the status and asks ready whether the root step is
// wanted. When it is, it takes the lock, asks for the password when
// Authorize is set, then reads and asks again: the step acts only on a
// read taken under the lock, once the password is given. The step is
// wanted when release is not nil, and the caller releases the lock when
// it ends.
func (a *Aerials) begin(ctx context.Context, ready func(Status) (bool, error)) (res Result, release func(), err error) {
	st := a.Status(ctx)
	res = Result{Before: st, After: st}
	if ctx.Err() != nil {
		return res, nil, errNotStarted
	}
	if ok, err := ready(st); !ok {
		return res, nil, err
	}
	if release, err = a.lock(); err != nil {
		return res, nil, err
	}
	// A hold further ahead than a step can run is from a clock set back.
	if until := a.heldUntil(); a.now().Before(until) && until.Sub(a.now()) <= stepLife {
		release()
		return res, nil, heldError{until}
	}
	if a.Authorize != nil {
		aerr := a.Authorize(ctx)
		switch {
		case ctx.Err() != nil:
			aerr = errNotStarted
		case aerr != nil:
			aerr = fmt.Errorf("authorization failed, nothing changed: %w", aerr)
		}
		if aerr != nil {
			release()
			return res, nil, aerr
		}
	}
	st = a.Status(ctx)
	if ctx.Err() != nil {
		release()
		return res, nil, errNotStarted
	}
	res = Result{Before: st, After: st}
	if ok, err := ready(st); !ok {
		release()
		return res, nil, err
	}
	return res, release, nil
}

// lock takes the lock beside the record, refusing at once while another
// run holds it, since that run may sit at a password prompt: two runs at
// once could each put a record back over the other's.
func (a *Aerials) lock() (func(), error) {
	if a.RecordPath == "" {
		return nil, errors.New("no place to keep the record: regrow's state folder is unknown")
	}
	if err := os.MkdirAll(filepath.Dir(a.RecordPath), 0o700); err != nil {
		return nil, fmt.Errorf("lock the record, nothing changed: %w", err)
	}
	f, err := os.OpenFile(a.RecordPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock the record, nothing changed: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock the record, nothing changed: %w", err)
	}
	return func() { _ = f.Close() }, nil
}

// hold writes into the lock file the time until which a root step may
// still run, or clears it for the zero time. A stop ends the command
// that runs the step, and the root shell or its sqlite3 may outlive it
// (sudo passes SIGTERM to the shell alone) or regrow, so the next off or
// on waits until the step has surely ended. Only the lock's holder
// writes it.
func (a *Aerials) hold(until time.Time) error {
	var data []byte
	if !until.IsZero() {
		data = []byte(until.UTC().Format(time.RFC3339Nano) + "\n")
	}
	return os.WriteFile(a.RecordPath+".lock", data, 0o600)
}

// heldUntil reads the time hold wrote; zero for none.
func (a *Aerials) heldUntil() time.Time {
	data, err := os.ReadFile(a.RecordPath + ".lock")
	if err != nil {
		return time.Time{}
	}
	until, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return until
}

// Off takes the lock, asks for the password, records the picks,
// journals the root step, clears the picks as root and stops the
// downloaders running then. With nothing picked it asks for nothing,
// changes nothing and keeps the record of the last off; with a record it cannot read it changes
// nothing, since writing would lose the picks that record keeps. A
// record still waiting for on keeps its picks beside the new ones. The
// root step's exit status decides the record: a step that says it
// changed nothing, or never ran, puts back the record as it was; any
// other end keeps it, since the step cleared the picks or may have.
func (a *Aerials) Off(ctx context.Context) (Result, error) {
	res, release, err := a.begin(ctx, func(st Status) (bool, error) {
		switch st.State {
		case StateOff:
			return false, nil
		case StateOn:
		default:
			return false, ErrNotApplicable
		}
		if st.RecordError != "" {
			return false, fmt.Errorf("read the record of the last off, nothing changed: %s", st.RecordError)
		}
		return true, nil
	})
	if release == nil {
		return res, err
	}
	defer release()
	picks := res.Before.Picks
	prev := res.Before.Recorded
	rec := Record{Cleared: a.now(), Picks: picks}
	if prev.Waiting() {
		rec.Picks = union(prev.Picks, picks)
	}
	if err := a.writeRecord(rec); err != nil {
		return res, fmt.Errorf("record the picks before clearing them, nothing changed: %w", err)
	}
	res, status, err := a.act(ctx, res, KindOff, offScript(a.db(), a.daemon(), picks))
	if status == exitNothing {
		if rerr := a.putRecordBack(prev); rerr != nil {
			err = errors.Join(err, fmt.Errorf("the record could not be put back as it was before this off: %w", rerr))
		}
	}
	return res, err
}

// putRecordBack makes the record what it was before an off: prev, or
// none.
func (a *Aerials) putRecordBack(prev *Record) error {
	if prev != nil {
		return a.writeRecord(*prev)
	}
	if err := os.Remove(a.RecordPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Gone counts the picks read before that are gone after.
func (r Result) Gone() int {
	return missing(r.Before.Picks, r.After.Picks)
}

// union is a's picks, then b's that a lacks, by their identifiers.
func union(a, b []Pick) []Pick {
	out := slices.Clone(a)
	for _, p := range b {
		if missing([]Pick{p}, out) == 1 {
			out = append(out, p)
		}
	}
	return out
}

// On puts the picks the last off cleared back, as root. With nothing
// recorded it asks for nothing and changes nothing. The record is marked
// restored once nothing is left to put back: after a step that exited 0,
// or, after one whose status says neither that nor that it changed
// nothing, when every pick it can carry is there afterwards. The
// downloads start again the next time macOS starts the downloader: at
// login, or when a wallpaper or screen saver setting changes.
func (a *Aerials) On(ctx context.Context) (Result, error) {
	res, release, err := a.begin(ctx, func(st Status) (bool, error) {
		switch st.State {
		case StateOn, StateOff:
		default:
			return false, ErrNotApplicable
		}
		if st.RecordError != "" {
			return false, fmt.Errorf("read the record of the last off, nothing changed: %s", st.RecordError)
		}
		if !st.Recorded.Waiting() {
			return false, nil
		}
		if !slices.ContainsFunc(st.Recorded.Picks, carryable) {
			return false, errors.New("the record of the last off names no pick regrow can put back; nothing changed")
		}
		return true, nil
	})
	if release == nil {
		return res, err
	}
	defer release()
	rec := res.Before.Recorded
	res, status, err := a.act(ctx, res, KindOn, onScript(a.db(), rec.Picks))
	read := res.After.State == StateOn || res.After.State == StateOff
	carried := slices.DeleteFunc(slices.Clone(rec.Picks), func(p Pick) bool { return !carryable(p) })
	if status != exitDone && (status == exitNothing || !read || missing(carried, res.After.Picks) > 0) {
		return res, err
	}
	if read {
		res.Missing = missing(rec.Picks, res.After.Picks)
	} else if err == nil {
		err = fmt.Errorf("the picks are back, but could not be read afterwards to count any not put back: %s", res.After.Detail)
	}
	restored := a.now()
	rec.Restored = &restored
	if werr := a.writeRecord(*rec); werr != nil {
		err = errors.Join(err, fmt.Errorf("the picks are back, but the record could not be marked restored: %w", werr))
	}
	return res, err
}

// act journals the root step, runs it, journals its end and reads the
// picks again. Its status is the step's exit status: exitNothing for a
// step that never ran, exitUnknown for one with no status.
func (a *Aerials) act(ctx context.Context, res Result, kind, script string) (Result, int, error) {
	argv := a.Root(script)
	runID := executor.NewRunID(a.now())
	start := oplog.Entry{
		Time: a.now(), Run: runID, Seq: 1, Event: oplog.EventStart,
		RuleID: RuleID, Kind: kind, Command: argv, Path: a.db(),
	}
	if err := a.hold(a.now().Add(stepLife)); err != nil {
		return res, exitNothing, fmt.Errorf("hold the lock for the root step, refusing to act: %w", err)
	}
	if err := a.Log.Append(start); err != nil {
		_ = a.hold(time.Time{})
		return res, exitNothing, fmt.Errorf("oplog append failed, refusing to act: %w", err)
	}
	res.Run = runID
	end := oplog.Entry{Time: a.now(), Run: runID, Seq: 1, Event: oplog.EventDone, RuleID: RuleID, Kind: kind}
	runErr := errNotStarted
	if ctx.Err() == nil {
		runErr = a.run(ctx, argv)
	}
	status := exitStatus(runErr)
	if status != exitUnknown {
		// The step has ended. A clear that fails only keeps the next run
		// waiting until the hold ends.
		_ = a.hold(time.Time{})
	} else {
		// One with no status may still run. It started before its command
		// returned, after any password prompt that command showed, so it
		// ends within stepLife of now.
		_ = a.hold(a.now().Add(stepLife))
	}
	notRun := errors.Is(runErr, ErrNotRun) || errors.Is(runErr, errNotStarted)
	if status == exitUnknown && ctx.Err() != nil {
		runErr = fmt.Errorf("%w (%v)", ErrStopped, runErr)
	}
	if runErr != nil {
		end.Event, end.Error = oplog.EventFail, runErr.Error()
	}
	if err := a.Log.Append(end); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("oplog append failed after the root step: %w", err))
	}
	if notRun {
		return res, status, runErr
	}
	res.Ran = status != exitNothing
	res.After = a.Status(context.WithoutCancel(ctx))
	switch {
	case runErr == nil, errors.Is(runErr, ErrStopped):
	case status == exitDone:
		// The step did it all; only the journal's end line failed.
	case status == exitNothing:
		runErr = fmt.Errorf("the root step changed nothing: %w", runErr)
	default:
		runErr = fmt.Errorf("the root step failed: %w", runErr)
	}
	return res, status, runErr
}

// missing counts recorded picks that are not among the picks now.
func missing(recorded, now []Pick) int {
	return len(gone(recorded, now))
}

// gone is the recorded picks that are not among the picks now.
func gone(recorded, now []Pick) []Pick {
	have := map[[3]string]bool{}
	for _, p := range now {
		have[[3]string{p.Table, p.ItemID, p.UserID}] = true
	}
	var out []Pick
	for _, p := range recorded {
		if !have[[3]string{p.Table, p.ItemID, p.UserID}] {
			out = append(out, p)
		}
	}
	return out
}

// offScript is the root step of off: one transaction deletes exactly
// the recorded picks, so a pick made since they were read stays and the
// next status shows it; then the downloaders running at that moment are
// stopped, since each keeps the picks it read when it started. launchd
// starts a new one on the next request, which reads the cleared picks.
// A downloader lives on briefly after SIGTERM, and one that outlives
// 5 s is killed. It exits exitDone, exitNothing or exitLived.
func offScript(db, daemon string, picks []Pick) string {
	var sql strings.Builder
	sql.WriteString("BEGIN IMMEDIATE;")
	for _, p := range picks {
		t, _ := tableNamed(p.Table)
		fmt.Fprintf(&sql, " DELETE FROM %s WHERE %s = %d AND %s = %d;", t.name, t.itemCol, p.Item, t.userCol, p.User)
	}
	sql.WriteString(" COMMIT;")
	return strings.Join([]string{
		"set -u",
		"/usr/bin/sqlite3 -bail -cmd '.timeout 5000' " + engine.ShellQuote(db) + " " + engine.ShellQuote(sql.String()) +
			" || { echo 'regrow: could not clear the aerial picks; nothing changed' >&2; exit " + strconv.Itoa(exitNothing) + "; }",
		"old=$(/usr/bin/pgrep -x " + engine.ShellQuote(daemon) + ")",
		`[ -n "$old" ] || exit 0`,
		`alive() { for p in $old; do kill -0 "$p" 2>/dev/null && return 0; done; return 1; }`,
		`wait_gone() { i=0; while [ "$i" -lt "$1" ]; do alive || return 0; sleep 0.1; i=$((i + 1)); done; ! alive; }`,
		`kill $old 2>/dev/null`,
		`wait_gone 50 && exit 0`,
		`kill -9 $old 2>/dev/null`,
		`wait_gone 20 && exit 0`,
		`echo "regrow: the picks are cleared, but the downloader ($old) did not exit; it may download for the old picks until it ends, at the latest when the Mac restarts" >&2`,
		"exit " + strconv.Itoa(exitLived),
	}, "\n")
}

// carryable says whether on's SQL can carry a recorded pick: a table it
// knows, and identifiers of the form the catalogue uses.
func carryable(p Pick) bool {
	_, ok := tableNamed(p.Table)
	return ok && identifier.MatchString(p.ItemID) && identifier.MatchString(p.UserID)
}

// onScript is the root step of on: one transaction inserts each
// recorded pick again by its identifiers. A pick whose category,
// subcategory, video or user is gone inserts nothing, and one the
// record names in a form the SQL cannot carry is left out; on counts
// both as missing. It exits exitDone or exitNothing.
func onScript(db string, picks []Pick) string {
	var sql strings.Builder
	sql.WriteString("BEGIN IMMEDIATE;")
	for _, p := range picks {
		if !carryable(p) {
			continue
		}
		t, _ := tableNamed(p.Table)
		fmt.Fprintf(&sql, " INSERT OR IGNORE INTO %s (%s, %s) SELECT i.Z_PK, u.Z_PK FROM %s i, ZUSER u WHERE i.ZIDENTIFIER = '%s' AND u.ZIDENTIFIER = '%s';",
			t.name, t.itemCol, t.userCol, t.itemTable, p.ItemID, p.UserID)
	}
	sql.WriteString(" COMMIT;")
	return strings.Join([]string{
		"set -u",
		"/usr/bin/sqlite3 -bail -cmd '.timeout 5000' " + engine.ShellQuote(db) + " " + engine.ShellQuote(sql.String()) +
			" || { echo 'regrow: could not put the aerial picks back; nothing changed' >&2; exit " + strconv.Itoa(exitNothing) + "; }",
	}, "\n")
}
