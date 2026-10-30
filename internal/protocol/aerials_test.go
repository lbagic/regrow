package protocol

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/aerials"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
)

// aerialsDB is a fixture Aerial.sqlite with one category picked.
func aerialsDB(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "Aerial.sqlite")
	const sql = `
CREATE TABLE ZUSER (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE ZCATEGORY (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE ZSUBCATEGORY (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE ZASSET (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE Z_2ACTIVEFORUSERS (Z_2ACTIVECATEGORIES INTEGER, Z_4ACTIVEFORUSERS INTEGER);
CREATE TABLE Z_3ACTIVEFORUSERS (Z_3ACTIVESUBCATEGORIES INTEGER, Z_4ACTIVEFORUSERS1 INTEGER);
CREATE TABLE Z_1PINNEDFORUSERS (Z_1PINNEDASSETS INTEGER, Z_4PINNEDFORUSERS INTEGER);
INSERT INTO ZUSER VALUES (1, '501');
INSERT INTO ZCATEGORY VALUES (1, 'AAAAAAAA-0000-0000-0000-000000000001');
INSERT INTO Z_2ACTIVEFORUSERS VALUES (1, 1);`
	if out, err := exec.Command("/usr/bin/sqlite3", "-bail", db, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	return db
}

type aerialsJournal struct{ entries []oplog.Entry }

func (j *aerialsJournal) Append(e oplog.Entry) error {
	j.entries = append(j.entries, e)
	return nil
}

// aerialsServer serves aerials on a fixture database; its root step
// runs root's script, or the given command, as the test's user.
func aerialsServer(t *testing.T, version string, root []string) (*Server, *aerialsJournal) {
	t.Helper()
	db := aerialsDB(t)
	record := filepath.Join(t.TempDir(), "aerials.json")
	lock := filepath.Join(t.TempDir(), "regrow-aerials.lock")
	journal := &aerialsJournal{}
	srv := newMachine(t).server()
	srv.Aerials = func() *aerials.Aerials {
		return &aerials.Aerials{
			DB: db, Version: version, RecordPath: record, Log: journal,
			Daemon:   "regrow-test-none",
			RootLock: lock,
			Root: func(script string) []string {
				if root != nil {
					return root
				}
				return []string{"/bin/sh", "-c", script}
			},
			Run: aerials.AdminRun(RunNative),
		}
	}
	return srv, journal
}

func TestAerialsStatusAndSwitch(t *testing.T) {
	srv, journal := aerialsServer(t, "15.7.7", nil)
	p := start(t, srv)
	p.expect("hello")

	p.send(`{"type":"aerials","id":"a1"}`)
	got := p.expect("aerials/a1", "done/a1")[0]
	if !strings.Contains(got.raw, `"status":{"state":"on"`) || !strings.Contains(got.raw, `"categories":1`) || strings.Contains(got.raw, `"result"`) {
		t.Errorf("status = %s", got.raw)
	}
	if len(journal.entries) != 0 {
		t.Errorf("a status read journaled %+v", journal.entries)
	}

	p.send(`{"type":"aerials_switch","id":"a2","to":"off"}`)
	got = p.expect("aerials/a2", "done/a2")[0]
	for _, want := range []string{`"status":{"state":"off"`, `"result":{"ran":true`, `"before":{"state":"on"`} {
		if !strings.Contains(got.raw, want) {
			t.Errorf("switch off = %s, missing %s", got.raw, want)
		}
	}
	if len(journal.entries) != 2 || journal.entries[0].Kind != aerials.KindOff {
		t.Errorf("journal = %+v, want the off's start and done", journal.entries)
	}

	p.send(`{"type":"aerials_switch","id":"a3","to":"on"}`)
	got = p.expect("aerials/a3", "done/a3")[0]
	if !strings.Contains(got.raw, `"status":{"state":"on"`) {
		t.Errorf("switch on = %s", got.raw)
	}

	p.send(`{"type":"aerials_switch","id":"a4","to":"maybe"}`)
	p.expectError("a4", CodeBadRequest)
}

func TestAerialsSwitchFailures(t *testing.T) {
	for _, c := range []struct {
		name, version string
		root          []string
		want          string
	}{
		{"unmeasured", "26.0", nil, "measured on macOS 15 only"},
		{"canceled", "15.7.7", []string{"/bin/sh", "-c", "echo '0:63: execution error: User canceled. (-128)' >&2; exit 1"}, "canceled at the password prompt; nothing changed"},
		{"changed nothing", "15.7.7", []string{"/bin/sh", "-c", "echo '0:24: execution error: The command exited with a non-zero status. (1)' >&2; exit 1"}, "the root step changed nothing"},
		{"downloader lived on", "15.7.7", []string{"/bin/sh", "-c", "echo '0:70: execution error: regrow: the downloader did not exit (2)' >&2; exit 1"}, "the root step failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := aerialsServer(t, c.version, c.root)
			p := start(t, srv)
			p.expect("hello")
			p.send(`{"type":"aerials_switch","id":"a1","to":"off"}`)
			p.expect("aerials/a1")
			if e := p.expectError("a1", CodeAerialsFailed); !strings.Contains(e.raw, c.want) {
				t.Errorf("error = %s, want %q", e.raw, c.want)
			}
		})
	}
}

// A status read holds no slot; a switch holds it as a tick does.
func TestAerialsBesideATick(t *testing.T) {
	srv, _ := aerialsServer(t, "15.7.7", nil)
	entered, release := make(chan struct{}), make(chan struct{})
	srv.Tick = func(context.Context, bool) (headroom.Tick, error) {
		close(entered)
		<-release
		return headroom.Tick{Sample: headroom.Sample{At: time.Now()}}, nil
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"tick","id":"t1"}`)
	waitFor(t, entered, "the tick to start")
	p.send(`{"type":"aerials","id":"a1"}`)
	p.expect("aerials/a1", "done/a1")
	p.send(`{"type":"aerials_switch","id":"a2","to":"off"}`)
	p.expectError("a2", CodeBusy)
	close(release)
	p.expect("tick/t1", "done/t1")
}

// A switch stopped while its root step runs ends saying the step may
// still finish, and keeps the record. The test holds the root lock, so
// the step waits in its first lines until the stop has ended its shell.
func TestAerialsSwitchStoppedMidStep(t *testing.T) {
	srv, journal := aerialsServer(t, "15.7.7", nil)
	dir := t.TempDir()
	lock, started := filepath.Join(dir, "root.lock"), filepath.Join(dir, "started")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// Closing it frees the lock, and the step's perl, left waiting, exits.
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	aerialsOf := srv.Aerials
	srv.Aerials = func() *aerials.Aerials {
		a := aerialsOf()
		a.RootLock = lock
		root := a.Root
		a.Root = func(script string) []string {
			return append([]string{"/bin/sh", "-c", `echo $$ >"$1.new" && mv "$1.new" "$1"; shift; exec "$@"`, "sh", started}, root(script)...)
		}
		return a
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"aerials_switch","id":"a1","to":"off"}`)
	pid := 0
	for begun := time.Now(); pid == 0; time.Sleep(10 * time.Millisecond) {
		if data, err := os.ReadFile(started); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if time.Since(begun) > waitLimit {
			t.Fatal("the root step never started")
		}
	}
	p.send(`{"type":"cancel","id":"c","target":"a1"}`)
	for begun := time.Now(); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Since(begun) > waitLimit {
			t.Fatal("the stop never ended the root step's shell")
		}
	}
	_ = f.Close()
	p.expect("aerials/a1")
	if e := p.expectError("a1", CodeAerialsFailed); !strings.Contains(e.raw, "stopped before the root step ended; it may still finish") {
		t.Errorf("error = %s, want the stop's words", e.raw)
	}
	p.expect("done/c")
	p.send(`{"type":"aerials","id":"a2"}`)
	got := p.expect("aerials/a2", "done/a2")[0]
	for _, want := range []string{`"status":{"state":"on"`, `"recorded":{"cleared":`} {
		if !strings.Contains(got.raw, want) {
			t.Errorf("status after the stop = %s, missing %s", got.raw, want)
		}
	}
	if strings.Contains(got.raw, `"restored"`) {
		t.Errorf("status after the stop = %s, want the record kept, not restored", got.raw)
	}
	if len(journal.entries) != 2 || journal.entries[1].Event != oplog.EventFail {
		t.Errorf("journal = %+v, want the off's start and fail", journal.entries)
	}
}
