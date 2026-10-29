package main

import (
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/oplog"
)

func TestHistoryLinesSayWhatTheSizeCounts(t *testing.T) {
	runs := oplog.Runs([]oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventStart, Kind: "trash", Bytes: 3 * gib},
		{Run: "r1", Seq: 1, Event: oplog.EventDone},
		{Run: "r1", Seq: 2, Event: oplog.EventStart, Kind: "trash", Bytes: 5 * gib},
		{Run: "r1", Seq: 2, Event: oplog.EventFail, Error: "finder said no"},
	})
	lines := historyLines(runs)
	if len(lines) != 3 {
		t.Fatalf("want one line per run and a two-line note, got %q", lines)
	}
	if !strings.Contains(lines[0], "3.0 GiB cleaned") || strings.Contains(lines[0], "8.0 GiB") {
		t.Errorf("a run's size must be labelled and count only the done action, got %q", lines[0])
	}
	for _, want := range []string{"Trash", "staging", "failed", "cut-short"} {
		if note := strings.Join(lines[1:], " "); !strings.Contains(note, want) {
			t.Errorf("the note must say what the size counts (%q missing), got %q", want, note)
		}
	}
}
