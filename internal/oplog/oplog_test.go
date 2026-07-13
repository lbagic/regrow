package oplog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/trash"
)

func t0(sec int) time.Time { return time.Date(2026, 7, 13, 12, 0, sec, 0, time.UTC) }

func TestAppendReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "oplog.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{Time: t0(0), Run: "r1", Seq: 1, Event: EventStart, RuleID: "npm-cache", Kind: "trash", Path: "/x", Bytes: 42, Command: []string{"osascript", "-e", "s"}},
		{Time: t0(1), Run: "r1", Seq: 1, Event: EventDone, RuleID: "npm-cache", Receipt: &trash.Receipt{Original: "/x", To: "/t/x", Method: trash.MethodFinder}},
	}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Event != EventStart || got[1].Receipt == nil || got[1].Receipt.To != "/t/x" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestReadMissingFileIsEmptyHistory(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || got != nil {
		t.Fatalf("missing journal must read as empty, got %v, %v", got, err)
	}
}

func TestReadCorruptLineFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oplog.jsonl")
	if err := os.WriteFile(path, []byte("{\"run\":\"r1\"}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("corrupt journal must not be silently skipped")
	}
}

func TestRunsGroupsAndOrders(t *testing.T) {
	entries := []Entry{
		{Time: t0(5), Run: "r2", Seq: 1, Event: EventStart},
		{Time: t0(0), Run: "r1", Seq: 1, Event: EventStart},
		{Time: t0(1), Run: "r1", Seq: 1, Event: EventDone},
	}
	runs := Runs(entries)
	if len(runs) != 2 || runs[0].ID != "r1" || runs[1].ID != "r2" || len(runs[0].Entries) != 2 {
		t.Fatalf("Runs = %+v", runs)
	}
}

func TestUndoableReverseOrderAndExcludesUndone(t *testing.T) {
	rc := func(p string) *trash.Receipt {
		return &trash.Receipt{Original: p, To: "/t" + p, Method: trash.MethodStaging}
	}
	r := Run{ID: "r1", Entries: []Entry{
		{Run: "r1", Seq: 1, Event: EventDone, Receipt: rc("/a")},
		{Run: "r1", Seq: 2, Event: EventDone, Receipt: rc("/b")},
		{Run: "r1", Seq: 3, Event: EventDone}, // native: no receipt, not undoable
		{Run: "r1", Seq: 4, Event: EventFail},
		{Run: "r1", Seq: 5, Event: EventDone, Receipt: rc("/c")},
		{Run: "r1", Seq: 2, Event: EventUndo}, // /b already restored
	}}
	got := r.Undoable()
	if len(got) != 2 || got[0].Receipt.Original != "/c" || got[1].Receipt.Original != "/a" {
		t.Fatalf("want [/c /a] (reverse order, /b undone, native skipped), got %+v", got)
	}

	// A failed undo attempt does not mark the action as undone.
	r.Entries = append(r.Entries, Entry{Run: "r1", Seq: 1, Event: EventUndo, Error: "locked"})
	if got := r.Undoable(); len(got) != 2 {
		t.Fatalf("failed undo must keep the action undoable, got %+v", got)
	}
}
