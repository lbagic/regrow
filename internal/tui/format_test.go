package tui

import (
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func TestActionLineStatesReversibility(t *testing.T) {
	tests := []struct {
		action engine.Action
		want   string
	}{
		{engine.Action{RuleID: "xcode-derived-data", Kind: engine.ActionTrash, Command: []string{"osascript"}}, "undo restores"},
		{engine.Action{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}}, "no undo"},
		{engine.Action{RuleID: "docker-volumes-named", Kind: engine.ActionNative, Command: []string{"docker", "volume", "rm", "v"},
			PreAction: engine.PreActionVolumeExport}, "no undo"},
	}
	for _, tt := range tests {
		if got := ActionLine(tt.action); !strings.Contains(got, tt.want) {
			t.Errorf("ActionLine(%s) = %q, want it to say %q", tt.action.RuleID, got, tt.want)
		}
	}
}

func TestTotalsLinesSplitByWhenSpaceReturns(t *testing.T) {
	plan := engine.Plan{Actions: []engine.Action{
		{Kind: engine.ActionNative, Bytes: 3 << 30},
		{Kind: engine.ActionTrash, Bytes: 1 << 30},
	}}
	lines := TotalsLines(plan)
	if len(lines) != 2 {
		t.Fatalf("want two subtotal lines, got %q", lines)
	}
	if !strings.HasPrefix(lines[0], "Frees now") || !strings.Contains(lines[0], "3.0 GiB") {
		t.Errorf("frees-now line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Frees after Trash") || !strings.Contains(lines[1], "1.0 GiB") {
		t.Errorf("after-trash line = %q", lines[1])
	}
}
