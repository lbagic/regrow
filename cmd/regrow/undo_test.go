package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// moved writes a moved copy at <dir>/moved/<name> unless gone, and
// returns its done line: what a Trash or staging move journals.
func moved(t *testing.T, dir, run string, seq int, at time.Time, name string, method trash.Method, gone bool) oplog.Entry {
	t.Helper()
	to := filepath.Join(dir, "moved", name)
	if !gone {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return oplog.Entry{Time: at, Run: run, Seq: seq, Event: oplog.EventDone, RuleID: "fixture",
		Receipt: &trash.Receipt{Original: filepath.Join(dir, "home", name), To: to, Method: method}}
}

// A move whose copy was emptied from the Trash, or taken out of regrow
// staging, is not counted as restorable, and the line says why.
func TestHistoryLinesCountOnlyCopiesOnDisk(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	runs := oplog.Runs([]oplog.Entry{
		moved(t, dir, "r1", 1, at, "emptied", trash.MethodFinder, true),
		moved(t, dir, "r2", 1, at.Add(time.Hour), "kept", trash.MethodFinder, false),
		moved(t, dir, "r2", 2, at.Add(time.Hour), "lost", trash.MethodStaging, true),
	})
	lines := historyLines(runs)
	if !strings.HasSuffix(lines[0], "(nothing restorable: 1 gone from the Trash)") {
		t.Errorf("a run whose Trash copy is gone must say so, got %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "(1 restorable, 1 gone from regrow staging)") {
		t.Errorf("a run with one copy left of two must count one, got %q", lines[1])
	}
}

// A bare undo passes over a newer run whose copies are all gone to
// restore an older one, and naming the newer run refuses it.
func TestUndoSkipsARunWithNothingLeftOnDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	at := time.Now()
	older := moved(t, dir, "r1", 1, at, "older", trash.MethodStaging, false)
	newer := moved(t, dir, "r2", 1, at.Add(time.Hour), "newer", trash.MethodFinder, true)
	log, err := oplog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []oplog.Entry{older, newer} {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(older.Receipt.Original); err != nil {
		t.Errorf("bare undo did not restore the older run: %v", err)
	}
	entries, err := oplog.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == oplog.EventUndo && e.Run != "r1" {
			t.Errorf("bare undo touched run %s: %+v", e.Run, e)
		}
	}

	err = runUndo([]string{"r2"})
	if err == nil || !strings.Contains(err.Error(), "nothing left to restore in run r2: 1 gone from the Trash") {
		t.Errorf("undo of a run whose copies are gone = %v, want it refused with the reason", err)
	}
}
