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
	if h.AfterTrash != 100 || h.Staged != 20 || h.Exported != 30 || h.FreesNow != 4+6 {
		t.Errorf("split = trash %d, staged %d, exported %d, now %d; want 100, 20, 30, 10", h.AfterTrash, h.Staged, h.Exported, h.FreesNow)
	}
	if h.Done != 5 || h.Failed != 1 || h.Undone != 1 || h.Prune {
		t.Errorf("counts = done %d, failed %d, undone %d, prune %v; want 5, 1, 1, false", h.Done, h.Failed, h.Undone, h.Prune)
	}
	want := map[string]int64{"caches": 120, "volumes": 30, "npm": 4, "gobuild": 6}
	for rule, bytes := range want {
		if h.Rules[rule] != bytes {
			t.Errorf("rules[%s] = %d, want %d (all: %v)", rule, h.Rules[rule], bytes, h.Rules)
		}
	}
	if len(h.Rules) != len(want) {
		t.Errorf("rules = %v, want only %v", h.Rules, want)
	}
	if got := h.Bytes(); got != 160 {
		t.Errorf("Bytes = %d, want the split's sum 160", got)
	}

	prunes := Runs([]Entry{
		{Run: "p1", Seq: 1, Event: EventStart, RuleID: "gobuild", Kind: "prune"},
		{Run: "p1", Seq: 1, Event: EventDone, Pruned: &Pruned{Files: 1, Bytes: 9}},
	})
	if p := prunes[0].Summary(); !p.Prune || p.FreesNow != 9 {
		t.Errorf("a run of prunes = prune %v, now %d; want true, 9", p.Prune, p.FreesNow)
	}
}
