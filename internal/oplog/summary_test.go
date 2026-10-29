package oplog

import (
	"testing"

	"github.com/lbagic/regrow/internal/trash"
)

func TestSummarySplitsByWhereTheSpaceWent(t *testing.T) {
	start := func(seq int, rule, kind string, bytes int64) Entry {
		return Entry{Run: "r1", Seq: seq, Event: EventStart, RuleID: rule, Kind: kind, Bytes: bytes}
	}
	done := func(seq int, method trash.Method) Entry {
		e := Entry{Run: "r1", Seq: seq, Event: EventDone}
		if method != "" {
			e.Receipt = &trash.Receipt{Original: "/gone", To: "/also-gone", Method: method}
		}
		return e
	}
	entries := []Entry{
		start(1, "caches", "trash", 100), done(1, trash.MethodFinder),
		start(2, "caches", "trash", 20), done(2, trash.MethodStaging),
		start(3, "volumes", "native", 30), done(3, trash.MethodExport),
		start(4, "npm", "native", 4), done(4, ""),
		start(5, "gobuild", "prune", 50), {Run: "r1", Seq: 5, Event: EventDone, Pruned: &Pruned{Files: 2, Bytes: 6}},
		start(6, "npm", "native", 9), {Run: "r1", Seq: 6, Event: EventFail, Error: "npm said no"},
		{Run: "r1", Seq: 1, Event: EventUndo},
	}
	h := Runs(entries)[0].Summary()
	// Seq 1's move was put back: it frees nothing.
	if h.AfterTrash != 0 || h.Staged != 20 || h.Exported != 30 || h.FreesNow != 4+6 {
		t.Errorf("split = trash %d, staged %d, exported %d, now %d; want 0, 20, 30, 10", h.AfterTrash, h.Staged, h.Exported, h.FreesNow)
	}
	if h.Done != 5 || h.Failed != 1 || h.Undone != 1 || h.Prune {
		t.Errorf("counts = done %d, failed %d, undone %d, prune %v; want 5, 1, 1, false", h.Done, h.Failed, h.Undone, h.Prune)
	}
	want := map[string]int64{"caches": 20, "volumes": 30, "npm": 4, "gobuild": 6}
	for rule, bytes := range want {
		if h.Rules[rule] != bytes {
			t.Errorf("rules[%s] = %d, want %d (all: %v)", rule, h.Rules[rule], bytes, h.Rules)
		}
	}
	if len(h.Rules) != len(want) {
		t.Errorf("rules = %v, want only %v", h.Rules, want)
	}
	if got := h.Bytes(); got != 60 {
		t.Errorf("Bytes = %d, want the split's sum 60", got)
	}

	failedUndo := Runs(append(entries[:len(entries)-1:len(entries)-1], Entry{Run: "r1", Seq: 1, Event: EventUndo, Error: "the copy is gone"}))
	if f := failedUndo[0].Summary(); f.AfterTrash != 100 || f.Undone != 0 {
		t.Errorf("after an undo that failed = trash %d, undone %d; want 100, 0", f.AfterTrash, f.Undone)
	}

	prunes := Runs([]Entry{
		{Run: "p1", Seq: 1, Event: EventStart, RuleID: "gobuild", Kind: "prune"},
		{Run: "p1", Seq: 1, Event: EventDone, Pruned: &Pruned{Files: 1, Bytes: 9}},
	})
	if p := prunes[0].Summary(); !p.Prune || p.FreesNow != 9 {
		t.Errorf("a run of prunes = prune %v, now %d; want true, 9", p.Prune, p.FreesNow)
	}
}

func TestSummaryBytesCountsOnlyFinishedActions(t *testing.T) {
	const gib = 1 << 30
	trashStart := func(seq int, bytes int64) Entry {
		return Entry{Run: "r1", Seq: seq, Event: EventStart, Kind: "trash", Bytes: bytes}
	}
	pruneStart := func(seq int, bytes int64) Entry {
		return Entry{Run: "r1", Seq: seq, Event: EventStart, Kind: "prune", Bytes: bytes}
	}
	done := func(seq int) Entry { return Entry{Run: "r1", Seq: seq, Event: EventDone} }
	fail := func(seq int) Entry {
		return Entry{Run: "r1", Seq: seq, Event: EventFail, Error: "finder said no"}
	}
	pruned := func(e Entry, bytes int64) Entry {
		e.Pruned = &Pruned{Files: 3, Bytes: bytes}
		return e
	}

	tests := []struct {
		name    string
		entries []Entry
		want    int64
	}{
		{"a done action counts its planned bytes", []Entry{trashStart(1, 100), done(1)}, 100},
		{
			"a failed action counts nothing beside a done one",
			[]Entry{trashStart(1, 100), done(1), trashStart(2, 50), fail(2)},
			100,
		},
		{
			"an action with no done line counts nothing: the run was cut short",
			[]Entry{trashStart(1, 100), done(1), trashStart(2, 50)},
			100,
		},
		{"a run cut short at its first start line counts nothing", []Entry{trashStart(1, 100)}, 0},
		{
			"a done prune counts what it measured, not its estimate",
			[]Entry{trashStart(1, 100), done(1), pruneStart(2, 41*gib), pruned(done(2), 7*gib)},
			100 + 7*gib,
		},
		{
			"a failed prune counts nothing, whatever it measured",
			[]Entry{pruneStart(1, 5*gib), pruned(fail(1), gib)},
			0,
		},
		{"a prune cut short counts nothing", []Entry{pruneStart(1, 5*gib)}, 0},
		{"a done prune with no measurement counts nothing", []Entry{pruneStart(1, 5*gib), done(1)}, 0},
		{
			"a move regrow undo put back counts nothing",
			[]Entry{trashStart(1, 100), done(1), trashStart(2, 50), done(2), {Run: "r1", Seq: 1, Event: EventUndo}},
			50,
		},
		{
			"a done line pairs with the start line of its own seq",
			[]Entry{trashStart(1, 100), trashStart(2, 50), fail(1), done(2)},
			50,
		},
	}
	for _, tt := range tests {
		runs := Runs(tt.entries)
		if len(runs) != 1 {
			t.Fatalf("%s: %d runs, want 1", tt.name, len(runs))
		}
		if got := runs[0].Summary().Bytes(); got != tt.want {
			t.Errorf("%s: Bytes = %d, want %d", tt.name, got, tt.want)
		}
	}
}
