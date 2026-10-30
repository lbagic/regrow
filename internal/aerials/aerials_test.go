package aerials

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/oplog"
)

// fixtureSchema is Aerial.sqlite's shape on macOS 15.7, cut to the
// columns the picks touch.
const fixtureSchema = `
PRAGMA journal_mode = WAL;
CREATE TABLE ZUSER (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, Z_OPT INTEGER, ZLASTACTIVEDATE TIMESTAMP, ZIDENTIFIER VARCHAR);
CREATE TABLE ZCATEGORY (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, Z_OPT INTEGER, ZIDENTIFIER VARCHAR);
CREATE TABLE ZSUBCATEGORY (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, Z_OPT INTEGER, ZCATEGORY INTEGER, ZIDENTIFIER VARCHAR);
CREATE TABLE ZASSET (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, Z_OPT INTEGER, ZIDENTIFIER VARCHAR);
CREATE TABLE Z_2ACTIVEFORUSERS (Z_2ACTIVECATEGORIES INTEGER, Z_4ACTIVEFORUSERS INTEGER, PRIMARY KEY (Z_2ACTIVECATEGORIES, Z_4ACTIVEFORUSERS));
CREATE TABLE Z_3ACTIVEFORUSERS (Z_3ACTIVESUBCATEGORIES INTEGER, Z_4ACTIVEFORUSERS1 INTEGER, PRIMARY KEY (Z_3ACTIVESUBCATEGORIES, Z_4ACTIVEFORUSERS1));
CREATE TABLE Z_1PINNEDFORUSERS (Z_1PINNEDASSETS INTEGER, Z_4PINNEDFORUSERS INTEGER, PRIMARY KEY (Z_1PINNEDASSETS, Z_4PINNEDFORUSERS));
INSERT INTO ZUSER VALUES (1, 4, 1, 0, '501');
INSERT INTO ZCATEGORY VALUES (1, 2, 1, 'AAAAAAAA-0000-0000-0000-000000000001'), (2, 2, 1, 'AAAAAAAA-0000-0000-0000-000000000002'), (3, 2, 1, 'AAAAAAAA-0000-0000-0000-000000000003');
INSERT INTO ZSUBCATEGORY VALUES (1, 3, 1, 1, 'BBBBBBBB-0000-0000-0000-000000000001');
INSERT INTO ZASSET VALUES (1, 1, 1, 'CCCCCCCC-0000-0000-0000-000000000001'), (2, 1, 1, 'CCCCCCCC-0000-0000-0000-000000000002');
INSERT INTO Z_2ACTIVEFORUSERS VALUES (1, 1), (2, 1);
INSERT INTO Z_3ACTIVEFORUSERS VALUES (1, 1);
INSERT INTO Z_1PINNEDFORUSERS VALUES (2, 1);
`

func sqlite(t *testing.T, db, sql string) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/sqlite3", "-bail", db, sql).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

type memLog struct {
	entries  []oplog.Entry
	fail     error
	onAppend func(oplog.Entry)
}

func (l *memLog) Append(e oplog.Entry) error {
	if l.fail != nil {
		return l.fail
	}
	l.entries = append(l.entries, e)
	if l.onAppend != nil {
		l.onAppend(e)
	}
	return nil
}

// fixture is an Aerials on a fixture database in a temp dir, whose
// root step runs as the test's user, and the commands it ran.
type fixture struct {
	a     *Aerials
	log   *memLog
	ran   [][]string
	asked int
	db    string
	dir   string
	asks  func()              // called when the password is asked for
	runs  func(argv []string) // called before each command runs
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{dir: dir, db: filepath.Join(dir, "Aerial.sqlite"), log: &memLog{}}
	sqlite(t, f.db, fixtureSchema)
	f.a = &Aerials{
		DB:         f.db,
		Version:    "15.7.7",
		RecordPath: filepath.Join(dir, "state", "aerials.json"),
		Log:        f.log,
		Authorize: func(context.Context) error {
			f.asked++
			if f.asks != nil {
				f.asks()
			}
			return nil
		},
		Root: func(script string) []string { return []string{"/bin/sh", "-c", script} },
		Run: func(ctx context.Context, argv []string) error {
			f.ran = append(f.ran, argv)
			if f.runs != nil {
				f.runs(argv)
			}
			out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
			}
			return nil
		},
		Daemon:   "regrow-test-none",
		RootLock: filepath.Join(dir, "root.lock"),
		Now:      func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) },
	}
	return f
}

func (f *fixture) record(t *testing.T) *Record {
	t.Helper()
	r, err := f.a.readRecord()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStatusReadsThePicks(t *testing.T) {
	f := newFixture(t)
	st := f.a.Status(context.Background())
	if st.State != StateOn {
		t.Fatalf("state = %q (%s), want on", st.State, st.Detail)
	}
	if want := (Counts{Categories: 2, Subcategories: 1, Videos: 1}); st.Counts != want {
		t.Errorf("counts = %+v, want %+v", st.Counts, want)
	}
	if got := st.Counts.Text(); got != "2 categories, 1 subcategory, 1 video" {
		t.Errorf("text = %q", got)
	}
	want := Pick{Table: "Z_2ACTIVEFORUSERS", Item: 2, User: 1, ItemID: "AAAAAAAA-0000-0000-0000-000000000002", UserID: "501"}
	found := false
	for _, p := range st.Picks {
		found = found || p == want
	}
	if !found {
		t.Errorf("picks %+v lack %+v", st.Picks, want)
	}
	if len(f.ran) != 0 || len(f.log.entries) != 0 {
		t.Errorf("status ran %v and journaled %v; it must only read", f.ran, f.log.entries)
	}
}

func TestStatusWhereItDoesNotApply(t *testing.T) {
	f := newFixture(t)
	f.a.Version = "26.1"
	if st := f.a.Status(context.Background()); st.State != StateUnmeasured || !strings.Contains(st.Detail, "macOS 26.1") {
		t.Errorf("macOS 26.1: %+v, want unmeasured naming the release", st)
	}
	f.a.Version = ""
	if st := f.a.Status(context.Background()); st.State != StateUnmeasured {
		t.Errorf("unknown release: %+v, want unmeasured", st)
	}
	f.a.Version = "15.7.7"
	f.a.DB = filepath.Join(f.dir, "absent.sqlite")
	if st := f.a.Status(context.Background()); st.State != StateAbsent {
		t.Errorf("no database: %+v, want absent", st)
	}
	f.a.DB = filepath.Join(f.dir, "other.sqlite")
	sqlite(t, f.a.DB, "CREATE TABLE ZUSER (Z_PK INTEGER PRIMARY KEY);")
	if st := f.a.Status(context.Background()); st.State != StateUnknown || !strings.Contains(st.Detail, "no such table") {
		t.Errorf("other tables: %+v, want unknown with sqlite's reason", st)
	}
}

func TestOffRecordsJournalsThenClears(t *testing.T) {
	f := newFixture(t)
	f.asks = func() {
		if r := f.record(t); r != nil || len(f.log.entries) != 0 {
			t.Errorf("record %+v and journal %+v at the password prompt, want neither", r, f.log.entries)
		}
	}
	f.runs = func([]string) {
		// The record and the start line are down before the root step.
		if r := f.record(t); r == nil || len(r.Picks) != 4 {
			t.Errorf("record before the root step = %+v, want the 4 picks", r)
		}
		if len(f.log.entries) != 1 || f.log.entries[0].Event != oplog.EventStart {
			t.Errorf("journal before the root step = %+v, want one start line", f.log.entries)
		}
	}
	res, err := f.a.Off(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || res.After.State != StateOff || res.Before.State != StateOn || f.asked != 1 {
		t.Fatalf("off: %+v, asked %d times; want one password prompt", res, f.asked)
	}
	if got := sqlite(t, f.db, "SELECT (SELECT count(*) FROM Z_2ACTIVEFORUSERS) + (SELECT count(*) FROM Z_3ACTIVEFORUSERS) + (SELECT count(*) FROM Z_1PINNEDFORUSERS)"); got != "0" {
		t.Errorf("picks left = %s, want 0", got)
	}
	if len(f.log.entries) != 2 {
		t.Fatalf("journal = %+v, want start and done", f.log.entries)
	}
	start, done := f.log.entries[0], f.log.entries[1]
	if start.Kind != KindOff || start.RuleID != RuleID || start.Path != f.db || start.Run != res.Run || done.Event != oplog.EventDone || done.Run != res.Run {
		t.Errorf("journal = %+v", f.log.entries)
	}
	if script := start.Command[len(start.Command)-1]; !strings.Contains(script, "DELETE FROM Z_1PINNEDFORUSERS WHERE Z_1PINNEDASSETS = 2 AND Z_4PINNEDFORUSERS = 1;") {
		t.Errorf("the journaled command does not name the pinned video it deletes:\n%s", script)
	}
	if r := f.record(t); r == nil || len(r.Picks) != 4 || r.Restored != nil || !r.Waiting() {
		t.Errorf("record = %+v, want the 4 picks waiting", r)
	}
	if st := f.a.Status(context.Background()); !st.Recorded.Waiting() {
		t.Errorf("status after off: %+v, want the record waiting", st)
	}
}

func TestSecondOffChangesNothing(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	ran, journaled := len(f.ran), len(f.log.entries)
	res, err := f.a.Off(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ran || len(f.ran) != ran || len(f.log.entries) != journaled || f.asked != 1 {
		t.Errorf("second off ran %v, journaled %v, asked %d times in all; want nothing and no password", f.ran[ran:], f.log.entries[journaled:], f.asked)
	}
	if r := f.record(t); r == nil || len(r.Picks) != 4 {
		t.Errorf("second off left the record %+v; want the first off's 4 picks", r)
	}
}

func TestOnPutsTheRecordedPicksBack(t *testing.T) {
	f := newFixture(t)
	before := f.a.Status(context.Background()).Picks
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := f.a.On(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || res.After.State != StateOn || res.Missing != 0 {
		t.Fatalf("on: %+v", res)
	}
	if len(res.After.Picks) != len(before) {
		t.Errorf("picks after on = %+v, want %+v", res.After.Picks, before)
	}
	last := f.log.entries[len(f.log.entries)-2:]
	if last[0].Kind != KindOn || last[0].Event != oplog.EventStart || last[1].Event != oplog.EventDone {
		t.Errorf("journal of on = %+v", last)
	}
	r := f.record(t)
	if r == nil || r.Restored == nil || r.Waiting() {
		t.Errorf("record after on = %+v, want it marked restored", r)
	}
	// A second on finds nothing waiting.
	ran := len(f.ran)
	res, err = f.a.On(context.Background())
	if err != nil || res.Ran || len(f.ran) != ran {
		t.Errorf("second on: %+v, %v; want nothing run", res, err)
	}
}

func TestOnWithNothingRecorded(t *testing.T) {
	f := newFixture(t)
	res, err := f.a.On(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ran || len(f.ran) != 0 || len(f.log.entries) != 0 || f.asked != 0 || res.After.State != StateOn {
		t.Errorf("on with no record: %+v, ran %v, journaled %v, asked %d; want nothing", res, f.ran, f.log.entries, f.asked)
	}
}

func TestOnFindsPicksByIdentifier(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The database numbers a category anew, and loses the subcategory.
	sqlite(t, f.db, "UPDATE ZCATEGORY SET Z_PK = 9 WHERE Z_PK = 2; DELETE FROM ZSUBCATEGORY;")
	res, err := f.a.On(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != 1 {
		t.Errorf("missing = %d, want the 1 subcategory", res.Missing)
	}
	if got := sqlite(t, f.db, "SELECT Z_2ACTIVECATEGORIES FROM Z_2ACTIVEFORUSERS ORDER BY 1"); got != "1\n9" {
		t.Errorf("categories picked = %q, want 1 and the renumbered 9", got)
	}
}

func TestOnSkipsARecordedPickTheSQLCannotCarry(t *testing.T) {
	f := newFixture(t)
	bad := Record{Cleared: time.Now(), Picks: []Pick{
		{Table: "Z_2ACTIVEFORUSERS", ItemID: "AAAAAAAA-0000-0000-0000-000000000003", UserID: "501"},
		{Table: "Z_2ACTIVEFORUSERS", ItemID: "x'; DROP TABLE ZUSER; --", UserID: "501"},
		{Table: "ZUSER", ItemID: "1", UserID: "501"},
	}}
	if err := f.a.writeRecord(bad); err != nil {
		t.Fatal(err)
	}
	res, err := f.a.On(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != 2 {
		t.Errorf("missing = %d, want the 2 unusable picks", res.Missing)
	}
	if got := sqlite(t, f.db, "SELECT count(*) FROM ZUSER"); got != "1" {
		t.Errorf("users = %s, want the table untouched", got)
	}
	if got := sqlite(t, f.db, "SELECT count(*) FROM Z_2ACTIVEFORUSERS WHERE Z_2ACTIVECATEGORIES = 3"); got != "1" {
		t.Errorf("the usable pick was not put back")
	}
}

func TestOffKeepsAPickMadeAfterItRead(t *testing.T) {
	f := newFixture(t)
	f.runs = func([]string) {
		sqlite(t, f.db, "INSERT INTO Z_2ACTIVEFORUSERS VALUES (3, 1);")
	}
	res, err := f.a.Off(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.After.State != StateOn || len(res.After.Picks) != 1 || res.After.Picks[0].Item != 3 {
		t.Errorf("after off = %+v, want only the new pick left", res.After)
	}
}

func TestNothingChangesWhereItDoesNotApply(t *testing.T) {
	f := newFixture(t)
	f.a.Version = "26.0"
	if err := f.a.writeRecord(Record{Cleared: time.Now(), Picks: []Pick{{Table: "Z_2ACTIVEFORUSERS", ItemID: "AAAAAAAA-0000-0000-0000-000000000003", UserID: "501"}}}); err != nil {
		t.Fatal(err)
	}
	for name, do := range map[string]func(context.Context) (Result, error){"off": f.a.Off, "on": f.a.On} {
		res, err := do(context.Background())
		if !errors.Is(err, ErrNotApplicable) {
			t.Errorf("%s on macOS 26: %v, want ErrNotApplicable", name, err)
		}
		if res.After.State != StateUnmeasured {
			t.Errorf("%s on macOS 26: after = %+v, want the status as it was", name, res.After)
		}
	}
	if len(f.ran) != 0 || len(f.log.entries) != 0 {
		t.Errorf("ran %v, journaled %v; want nothing", f.ran, f.log.entries)
	}
}

func TestOffRefusesWithoutTheJournal(t *testing.T) {
	f := newFixture(t)
	f.log.fail = errors.New("disk full")
	if _, err := f.a.Off(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to act") {
		t.Fatalf("off with a broken journal: %v", err)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %v with no journal", f.ran)
	}
	if got := sqlite(t, f.db, "SELECT count(*) FROM Z_2ACTIVEFORUSERS"); got != "2" {
		t.Errorf("picks changed with no journal")
	}
}

func TestOffRefusesWithNowhereToRecord(t *testing.T) {
	f := newFixture(t)
	f.a.RecordPath = ""
	if _, err := f.a.Off(context.Background()); err == nil || len(f.ran) != 0 || len(f.log.entries) != 0 {
		t.Errorf("off with no record path: %v, ran %v, journaled %v; want an error and nothing done", err, f.ran, f.log.entries)
	}
}

func TestOffJournalsAFailedRootStep(t *testing.T) {
	f := newFixture(t)
	f.a.Root = func(string) []string {
		return []string{"/bin/sh", "-c", "echo 'sudo: a password is required' >&2; exit 1"}
	}
	res, err := f.a.Off(context.Background())
	if err == nil || !strings.Contains(err.Error(), "a password is required") || !strings.Contains(err.Error(), "changed nothing") {
		t.Fatalf("off: %v", err)
	}
	if res.Ran || res.After.State != StateOn {
		t.Errorf("result = %+v, want no change reported and the picks as they were", res)
	}
	if last := f.log.entries[len(f.log.entries)-1]; last.Event != oplog.EventFail || !strings.Contains(last.Error, "a password is required") {
		t.Errorf("journal = %+v, want a fail line", f.log.entries)
	}
}

// A second off, after a pick made once the first read the picks or one
// made since, keeps what the first cleared: on puts every pick back.
func TestSecondOffKeepsTheFirstOnesPicks(t *testing.T) {
	f := newFixture(t)
	before := f.a.Status(context.Background()).Picks
	f.runs = func([]string) { sqlite(t, f.db, "INSERT INTO Z_2ACTIVEFORUSERS VALUES (3, 1);") }
	if res, err := f.a.Off(context.Background()); err != nil || res.After.State != StateOn {
		t.Fatalf("first off: %+v, %v; want the new pick left", res, err)
	}
	f.runs = nil
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := f.record(t); r == nil || len(r.Picks) != len(before)+1 {
		t.Fatalf("record after two offs = %+v, want the %d picks of both", r, len(before)+1)
	}
	res, err := f.a.On(context.Background())
	if err != nil || res.Missing != 0 || len(res.After.Picks) != len(before)+1 {
		t.Errorf("on: %+v, %v; want every pick both offs cleared back", res.After.Picks, err)
	}
}

// A root step that failed and cleared nothing leaves the record as it
// was: the earlier off's, or none.
func TestOffThatClearedNothingKeepsTheRecord(t *testing.T) {
	f := newFixture(t)
	refuse := func(string) []string {
		return []string{"/bin/sh", "-c", "echo 'sudo: a password is required' >&2; exit 1"}
	}
	root := f.a.Root
	f.a.Root = refuse
	if _, err := f.a.Off(context.Background()); err == nil {
		t.Fatal("off with a refused password succeeded")
	}
	if r := f.record(t); r != nil {
		t.Errorf("record after a refused first off = %+v, want none", r)
	}

	f.a.Root = root
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := f.record(t)
	sqlite(t, f.db, "INSERT INTO Z_2ACTIVEFORUSERS VALUES (3, 1);")
	f.a.Root = refuse
	if _, err := f.a.Off(context.Background()); err == nil {
		t.Fatal("off with a refused password succeeded")
	}
	if r := f.record(t); r == nil || len(r.Picks) != len(first.Picks) || !r.Cleared.Equal(first.Cleared) {
		t.Errorf("record after a refused second off = %+v, want the first off's %+v", r, first)
	}
}

// The password is asked for before the picks are read: a pick taken
// away at the prompt is not recorded, and on does not pick it again;
// with every pick taken away, off has nothing to do.
func TestOffReadsThePicksOnceThePasswordIsGiven(t *testing.T) {
	f := newFixture(t)
	f.asks = func() { sqlite(t, f.db, "DELETE FROM Z_2ACTIVEFORUSERS WHERE Z_2ACTIVECATEGORIES = 1;") }
	res, err := f.a.Off(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Before.Picks) != 3 {
		t.Errorf("picks read before the step = %+v, want the 3 left after the prompt", res.Before.Picks)
	}
	if r := f.record(t); r == nil || len(r.Picks) != 3 {
		t.Errorf("record = %+v, want the 3 picks the step cleared", r)
	}
	f.asks = nil
	if _, err := f.a.On(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sqlite(t, f.db, "SELECT count(*) FROM Z_2ACTIVEFORUSERS WHERE Z_2ACTIVECATEGORIES = 1"); got != "0" {
		t.Errorf("on picked again the category taken away at the prompt")
	}

	g := newFixture(t)
	g.asks = func() {
		sqlite(t, g.db, "DELETE FROM Z_2ACTIVEFORUSERS; DELETE FROM Z_3ACTIVEFORUSERS; DELETE FROM Z_1PINNEDFORUSERS;")
	}
	res, err = g.a.Off(context.Background())
	if err != nil || res.Ran || res.Before.State != StateOff || len(g.ran) != 0 || len(g.log.entries) != 0 || g.record(t) != nil {
		t.Errorf("off with every pick taken away at the prompt: %+v, %v, ran %v; want nothing done", res, err, g.ran)
	}
}

// The root step's exit status decides the record: 1 says nothing
// changed and puts it back; 2 (the picks cleared, a downloader lived
// on), a status the script does not give, and a signal keep it, with
// every pick, so a pick made again before the read afterwards is still
// in it, and on puts every pick back.
func TestOffDecidesTheRecordByExitStatus(t *testing.T) {
	for _, c := range []struct {
		name, tail string
		kept       bool
	}{
		{"exit 2", "echo 'the downloader did not exit' >&2; exit 2", true},
		{"exit 7", "exit 7", true},
		{"a signal", "kill -KILL $$", true},
		{"exit 1", "", false},
	} {
		f := newFixture(t)
		before := f.a.Status(context.Background()).Picks
		f.a.Root = func(script string) []string {
			if !c.kept {
				return []string{"/bin/sh", "-c", "exit 1"}
			}
			return []string{"/bin/sh", "-c", "(" + script + ")\n/usr/bin/sqlite3 '" + f.db + "' 'INSERT INTO Z_2ACTIVEFORUSERS VALUES (1, 1);'\n" + c.tail}
		}
		res, err := f.a.Off(context.Background())
		if err == nil || errors.Is(err, ErrStopped) || res.Ran != c.kept {
			t.Errorf("%s: %+v, %v; want a failed step, reported run when it may have changed the picks", c.name, res, err)
		}
		r := f.record(t)
		if !c.kept {
			if r != nil {
				t.Errorf("%s: record = %+v, want none", c.name, r)
			}
			continue
		}
		if r == nil || len(r.Picks) != len(before) {
			t.Errorf("%s: record = %+v, want all %d picks", c.name, r, len(before))
			continue
		}
		f.a.Root = func(script string) []string { return []string{"/bin/sh", "-c", script} }
		if res, err := f.a.On(context.Background()); err != nil || len(res.After.Picks) != len(before) {
			t.Errorf("%s: on: %+v, %v; want all %d picks", c.name, res.After.Picks, err, len(before))
		}
	}
}

// A runner that did not start the step, as at the Cancel of macOS's
// password prompt, changed nothing: the record goes back, and a pick
// taken away meanwhile is not picked again.
func TestOffThatDidNotRunPutsTheRecordBack(t *testing.T) {
	f := newFixture(t)
	f.a.Authorize = nil
	f.a.Run = func(context.Context, []string) error {
		sqlite(t, f.db, "DELETE FROM Z_2ACTIVEFORUSERS WHERE Z_2ACTIVECATEGORIES = 2;")
		return fmt.Errorf("%w: execution error: User canceled. (-128)", ErrNotRun)
	}
	res, err := f.a.Off(context.Background())
	if !errors.Is(err, ErrNotRun) || res.Ran {
		t.Fatalf("off not run: %+v, %v; want ErrNotRun and no step run", res, err)
	}
	if r := f.record(t); r != nil {
		t.Errorf("record = %+v, want none", r)
	}
	if e := f.log.entries; len(e) != 2 || e[1].Event != oplog.EventFail || !strings.Contains(e[1].Error, "nothing changed") {
		t.Errorf("journal = %+v, want its start, then a fail line saying nothing changed", e)
	}
	if res, err := f.a.On(context.Background()); err != nil || res.Ran {
		t.Errorf("on after an off that did not run: %+v, %v; want nothing to put back", res, err)
	}
}

// Stopping off while its root step runs cannot say whether the step
// went on: the journal and the error say so, and the record stays.
func TestOffStoppedMidStep(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.a.Run = func(ctx context.Context, argv []string) error {
		if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("the step: %v: %s", err, out)
		}
		cancel()
		return errors.New("signal: terminated")
	}
	_, err := f.a.Off(ctx)
	if !errors.Is(err, ErrStopped) || strings.Contains(err.Error(), "failed") {
		t.Fatalf("off stopped mid-step: %v, want ErrStopped", err)
	}
	if last := f.log.entries[len(f.log.entries)-1]; last.Event != oplog.EventFail || !strings.Contains(last.Error, "may still finish") {
		t.Errorf("journal = %+v, want a fail line saying the step may still finish", f.log.entries)
	}
	if r := f.record(t); !r.Waiting() {
		t.Errorf("record after a stopped off = %+v, want it kept for on", r)
	}
}

// holdRootLock leaves what a stopped step may leave: its shell, which
// took the root lock, gone, and a child of it holding the lock for d.
func holdRootLock(t *testing.T, lock string, d time.Duration) {
	t.Helper()
	script := strings.Join(append(lockLines(lock), fmt.Sprintf("/bin/sleep %g </dev/null >/dev/null 2>&1 &", d.Seconds())), "\n")
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("take the root lock: %v: %s", err, out)
	}
}

// A root step that outlives its shell, regrow or the app holds the root
// lock for as long as any of its processes runs, and the next step waits
// for it, so a stopped off's sqlite3 cannot clear picks an on has just
// put back.
func TestARootStepWaitsForAnEarlierOne(t *testing.T) {
	f := newFixture(t)
	before := f.a.Status(context.Background()).Picks
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	holdRootLock(t, f.a.RootLock, 2*time.Second)
	start := time.Now()
	res, err := f.a.On(context.Background())
	if err != nil || !res.Ran || len(res.After.Picks) != len(before) {
		t.Fatalf("on while an earlier step holds the root lock: %+v, %v; want all %d picks back once it ends", res.After.Picks, err, len(before))
	}
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("on's step ran %v after the earlier one took the root lock, want it to wait for it", waited)
	}
}

// A step that cannot take the root lock within rootLockWait changes
// nothing, and on keeps the record for the next on.
func TestARootStepGivesUpOnALockStillHeld(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	wait := rootLockWait
	rootLockWait = 1
	defer func() { rootLockWait = wait }()
	holdRootLock(t, f.a.RootLock, 3*time.Second)
	res, err := f.a.On(context.Background())
	if err == nil || res.Ran || !strings.Contains(err.Error(), "could not take the lock") {
		t.Errorf("on while the root lock stays held: %+v, %v; want nothing changed", res, err)
	}
	if r := f.record(t); !r.Waiting() {
		t.Errorf("record = %+v, want it still waiting for on", r)
	}
}

// A stop that comes after off wrote its record and journaled, but
// before the root step ran, changes nothing: the record goes back.
func TestOffStoppedBeforeTheStep(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.log.onAppend = func(oplog.Entry) { cancel() }
	res, err := f.a.Off(ctx)
	if err == nil || errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), "nothing changed") || len(f.ran) != 0 || res.Ran {
		t.Fatalf("off stopped before its step: %+v, %v, ran %v; want nothing run and nothing changed", res, err, f.ran)
	}
	if r := f.record(t); r != nil {
		t.Errorf("record after off stopped before its step = %+v, want none", r)
	}
	if e := f.log.entries; len(e) != 2 || e[0].Event != oplog.EventStart || e[1].Event != oplog.EventFail || !strings.Contains(e[1].Error, "nothing changed") {
		t.Errorf("journal = %+v, want its start, then a fail line saying nothing changed", e)
	}
}

// A stop at the password prompt or just after it, or a password not
// given, writes no record, journals nothing and runs no step.
func TestOffStoppedOrRefusedAtThePrompt(t *testing.T) {
	for _, c := range []struct {
		name string
		stop bool
		err  error
	}{
		{"stopped at the prompt", true, errors.New("signal: interrupt")},
		{"stopped once the password was given", true, nil},
		{"refused", false, errors.New("sudo: 3 incorrect password attempts: exit status 1")},
	} {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.a.Authorize = func(context.Context) error {
			if c.stop {
				cancel()
			}
			return c.err
		}
		res, err := f.a.Off(ctx)
		cancel()
		if err == nil || errors.Is(err, ErrStopped) || errors.Is(err, ErrNotApplicable) || !strings.Contains(err.Error(), "nothing changed") || res.Ran {
			t.Errorf("%s: off: %+v, %v; want no step run and nothing changed", c.name, res, err)
		}
		if r := f.record(t); r != nil || len(f.log.entries) != 0 || len(f.ran) != 0 {
			t.Errorf("%s: record %+v, journal %+v, ran %v; want none", c.name, r, f.log.entries, f.ran)
		}
	}
}

// A stop that lands while off or on first reads the picks says nothing
// changed, not that the picks cannot be read.
func TestAStopWhileReadingChangesNothing(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, do := range map[string]func(context.Context) (Result, error){"off": f.a.Off, "on": f.a.On} {
		res, err := do(ctx)
		if err == nil || errors.Is(err, ErrNotApplicable) || !strings.Contains(err.Error(), "nothing changed") || res.Ran {
			t.Errorf("%s stopped while reading: %+v, %v; want no step run and nothing changed", name, res, err)
		}
	}
	if r := f.record(t); r != nil || f.asked != 0 || len(f.log.entries) != 0 || len(f.ran) != 0 {
		t.Errorf("record %+v, asked %d, journal %+v, ran %v; want none", r, f.asked, f.log.entries, f.ran)
	}
}

// The step acts only on a read taken under the lock, with Authorize nil
// too: an off that ran whole between another run's first read and its
// lock is seen, so its record is not overwritten with an older one.
func TestTheStepActsOnAReadTakenUnderTheLock(t *testing.T) {
	f := newFixture(t)
	f.a.Authorize = nil
	other := *f.a
	reads := 0
	res, release, err := f.a.begin(context.Background(), func(Status) (bool, error) {
		if reads++; reads == 1 {
			if _, err := other.Off(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		return true, nil
	})
	if err != nil || release == nil {
		t.Fatalf("begin: %v", err)
	}
	release()
	if r := res.Before.Recorded; res.Before.State != StateOff || r == nil || len(r.Picks) != 4 {
		t.Errorf("read the step acts on = %+v, want the picks and record as the other off left them", res.Before)
	}
}

// One off or on at a time: a second refuses while the first holds the
// lock, from its password prompt to its end, so neither puts a record
// back over the other's.
func TestOneOffOrOnAtATime(t *testing.T) {
	f := newFixture(t)
	other := *f.a
	other.Run = func(context.Context, []string) error {
		t.Error("the second run ran a root step")
		return nil
	}
	check := func(when string) {
		for name, do := range map[string]func(context.Context) (Result, error){"off": other.Off, "on": other.On} {
			if res, err := do(context.Background()); !errors.Is(err, ErrBusy) || res.Ran {
				t.Errorf("%s %s: %+v, %v; want ErrBusy", name, when, res, err)
			}
		}
	}
	if err := f.a.writeRecord(Record{Cleared: time.Now(), Picks: []Pick{{Table: "Z_2ACTIVEFORUSERS", ItemID: "AAAAAAAA-0000-0000-0000-000000000003", UserID: "501"}}}); err != nil {
		t.Fatal(err)
	}
	f.asks = func() { check("at the first's password prompt") }
	f.runs = func([]string) { check("during the first's step") }
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.asks, f.runs = nil, nil
	if r := f.record(t); r == nil || len(r.Picks) != 5 {
		t.Errorf("record = %+v, want the earlier pick and the 4 cleared", r)
	}
	if res, err := f.a.On(context.Background()); err != nil || !res.Ran {
		t.Errorf("on after the first off ended: %+v, %v; want the lock free", res, err)
	}
}

// on stopped after its step put every pick it can carry back has
// finished: the record is marked restored, so a later on does not pick
// them again, and a pick it could not carry is counted missing.
func TestOnStoppedAfterItsStep(t *testing.T) {
	f := newFixture(t)
	sqlite(t, f.db, "INSERT INTO Z_2ACTIVEFORUSERS VALUES (99, 1);")
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.a.Run = func(ctx context.Context, argv []string) error {
		if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("the step: %v: %s", err, out)
		}
		cancel()
		return errors.New("signal: terminated")
	}
	res, err := f.a.On(ctx)
	if !errors.Is(err, ErrStopped) || res.Missing != 1 {
		t.Fatalf("on stopped after its step: %+v, %v; want ErrStopped and the 1 pick with no item missing", res, err)
	}
	if r := f.record(t); r == nil || r.Waiting() {
		t.Errorf("record after on stopped after its step = %+v, want it marked restored", r)
	}
}

// An on whose step says it changed nothing keeps the record waiting.
func TestOnThatChangedNothingKeepsTheRecord(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.a.Root = func(string) []string { return []string{"/bin/sh", "-c", "exit 1"} }
	if res, err := f.a.On(context.Background()); err == nil || !strings.Contains(err.Error(), "changed nothing") || res.Ran || res.After.State != StateOff {
		t.Errorf("on that changed nothing: %+v, %v", res, err)
	}
	if r := f.record(t); !r.Waiting() {
		t.Errorf("record = %+v, want it still waiting", r)
	}
}

// on counts a pick as missing only from picks it could read afterwards,
// marking the record restored after a step that exited 0, and runs no
// root step for a record with nothing it can carry.
func TestOnEdges(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := f.a.Run
	f.a.Run = func(ctx context.Context, argv []string) error {
		err := run(ctx, argv)
		if cerr := os.Chmod(f.db, 0); cerr != nil {
			t.Fatal(cerr)
		}
		return err
	}
	t.Cleanup(func() { _ = os.Chmod(f.db, 0o644) })
	res, err := f.a.On(context.Background())
	if os.Geteuid() != 0 && (err == nil || res.Missing != 0) {
		t.Errorf("on with the picks unreadable after: %+v, %v; want an error and none counted missing", res, err)
	}
	if r := f.record(t); r == nil || r.Waiting() {
		t.Errorf("record after on with the picks unreadable after = %+v, want it marked restored", r)
	}

	g := newFixture(t)
	if err := g.a.writeRecord(Record{Cleared: time.Now(), Picks: []Pick{{Table: "Z_2ACTIVEFORUSERS", UserID: "501"}}}); err != nil {
		t.Fatal(err)
	}
	if res, err := g.a.On(context.Background()); err == nil || res.Ran || len(g.ran) != 0 || len(g.log.entries) != 0 {
		t.Errorf("on with nothing it can carry: %+v, %v, ran %v; want an error and nothing run or journaled", res, err, g.ran)
	}
}

func TestReadRecordOfAnotherShapeIsAnError(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.a.RecordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.a.RecordPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.On(context.Background()); err == nil || len(f.ran) != 0 {
		t.Errorf("on with a broken record: %v, ran %v; want an error and nothing run", err, f.ran)
	}
	// off would write over the picks that record keeps.
	if _, err := f.a.Off(context.Background()); err == nil || len(f.ran) != 0 || len(f.log.entries) != 0 {
		t.Errorf("off with a broken record: %v, ran %v; want an error and nothing run or journaled", err, f.ran)
	}
	if data, err := os.ReadFile(f.a.RecordPath); err != nil || string(data) != "{" {
		t.Errorf("record after off = %q, %v; want it as it was", data, err)
	}
	var probe Record
	if err := json.Unmarshal([]byte(`{"cleared":"2026-10-29T12:00:00Z","picks":[]}`), &probe); err != nil || probe.Waiting() {
		t.Errorf("an empty record waits: %+v", probe)
	}
}

// A stop sends the root step SIGTERM, which sudo needs to put the
// terminal's echo back and to pass on to its shell, not SIGKILL.
func TestRunRootStopsWithSIGTERM(t *testing.T) {
	dir := t.TempDir()
	ready, term := filepath.Join(dir, "ready"), filepath.Join(dir, "term")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunRoot(ctx, []string{"/bin/sh", "-c", `trap ': >"$2"; exit 1' TERM; : >"$1"; while :; do sleep 0.05; done`, "sh", ready, term}, io.Discard, io.Discard)
	}()
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("the step never started")
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("a stopped step returned no error")
	}
	if _, err := os.Stat(term); err != nil {
		t.Errorf("the step saw no SIGTERM: %v", err)
	}
	if err := RunRoot(context.Background(), []string{filepath.Join(dir, "absent")}, io.Discard, io.Discard); !errors.Is(err, ErrNotRun) {
		t.Errorf("a command that cannot start: %v, want ErrNotRun", err)
	}
	// A step that ignores the stop and exits 0 has finished.
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := RunRoot(ctx, []string{"/bin/sh", "-c", "trap '' TERM; sleep 0.5; exit 0"}, io.Discard, io.Discard); err != nil {
		t.Errorf("a step that exited 0 after a stop: %v, want no error", err)
	}
}

// fakeDaemon starts a system program under a name of its own,
// so the root step's pgrep finds it and nothing else. With ignoreTerm it
// outlives SIGTERM, as idleassetsd does for about half a second. It is
// reaped as soon as it exits, since the root step reads a zombie as
// alive; exited closes then.
func fakeDaemon(t *testing.T, ignoreTerm bool) (name string, exited <-chan struct{}) {
	t.Helper()
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name = "rgaer" + hex.EncodeToString(b)
	dir := t.TempDir()
	program := filepath.Join(dir, name)
	src, args := "/bin/sleep", []string{"30"}
	if ignoreTerm {
		fifo := filepath.Join(dir, "fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		src, args = "/bin/bash", []string{"-c", "trap '' TERM; exec 3<>" + fifo + "; read -t 30 <&3"}
	}
	// A link, since macOS kills a copy of a system program at launch;
	// the process takes the link's name.
	if err := os.Symlink(src, program); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(program, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	for i := 0; i < 50; i++ {
		if exec.Command("/usr/bin/pgrep", "-x", name).Run() == nil {
			return name, done
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the fake downloader %s did not start", name)
	return "", nil
}

func TestOffStopsTheDownloader(t *testing.T) {
	for _, ignoreTerm := range []bool{false, true} {
		f := newFixture(t)
		name, exited := fakeDaemon(t, ignoreTerm)
		f.a.Daemon = name
		if _, err := f.a.Off(context.Background()); err != nil {
			t.Fatalf("ignoreTerm %v: %v", ignoreTerm, err)
		}
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			t.Errorf("ignoreTerm %v: the downloader is still running after off", ignoreTerm)
		}
	}
}
