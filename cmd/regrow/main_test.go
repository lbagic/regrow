package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
)

func TestSelectionFor(t *testing.T) {
	findings := []engine.Finding{
		{Rule: engine.Rule{ID: "safe-found", Risk: engine.RiskSafe}, Items: []engine.Item{{Path: "/u/a", Bytes: 1}}},
		{Rule: engine.Rule{ID: "caution-found", Risk: engine.RiskCaution}, Items: []engine.Item{{Path: "/u/b", Bytes: 1}}},
	}
	tests := []struct {
		name string
		ids  []string
		want map[string]bool
	}{
		{"no ids: default selection", nil, map[string]bool{"safe-found": true}},
		{"ids as given", []string{"caution-found", "safe-found/~/x"}, map[string]bool{"caution-found": true, "safe-found/~/x": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectionFor(tt.ids, findings); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("selectionFor(%v) = %v, want %v", tt.ids, got, tt.want)
			}
		})
	}
}

func TestParseArgsHelp(t *testing.T) {
	tests := []struct {
		args     []string
		wantCmd  string
		wantHelp bool
		wantIDs  []string
	}{
		{[]string{"help"}, "help", true, nil},
		{[]string{"--help"}, "scan", true, nil},
		{[]string{"-h"}, "scan", true, nil},
		{[]string{"plan", "--help"}, "plan", true, nil},
		{[]string{"clean", "--yes", "go-build-cache"}, "clean", false, []string{"go-build-cache"}},
	}
	for _, tt := range tests {
		cmd, _, ids, help, err := parseArgs(tt.args)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", tt.args, err)
		}
		if len(ids) == 0 {
			ids = nil
		}
		if cmd != tt.wantCmd || help != tt.wantHelp || !reflect.DeepEqual(ids, tt.wantIDs) {
			t.Errorf("parseArgs(%v) = cmd %q help %v ids %v, want %q %v %v", tt.args, cmd, help, ids, tt.wantCmd, tt.wantHelp, tt.wantIDs)
		}
	}
	if _, _, _, _, err := parseArgs([]string{"--frobnicate"}); err == nil || !strings.Contains(err.Error(), "regrow help") {
		t.Fatalf("an unknown flag must point at `regrow help`, got %v", err)
	}
}

func TestUsageListsEverySubcommand(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	for _, want := range []string{
		"regrow [scan]", "regrow plan", "regrow clean", "regrow doctor", "regrow undo",
		"regrow history", "regrow rules", "regrow version", "regrow help",
		"-json", "-yes", "-rules-dir", "-beta-rules",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage is missing %q:\n%s", want, out)
		}
	}
}

func TestNothingToCleanShowsSkipReasons(t *testing.T) {
	plan := engine.Plan{Skipped: []engine.Skip{
		{RuleID: "lib", ItemKey: "~/lib", Reason: "contains keep/~/lib/backup, which regrow never deletes"},
	}}
	out := strings.Join(nothingToClean(plan), "\n")
	if !strings.Contains(out, "lib/~/lib") || !strings.Contains(out, "contains keep/~/lib/backup, which regrow never deletes") {
		t.Fatalf("an all-skipped plan must say why, got:\n%s", out)
	}
	if got := nothingToClean(engine.Plan{}); len(got) != 1 || !strings.Contains(got[0], "found anything") {
		t.Fatalf("an empty plan = %q", got)
	}
}

func TestRunSummarySeparatesStagingFromTrash(t *testing.T) {
	res := executor.Result{Done: 3, Bytes: 70 << 30, TrashBytes: 0, StagedBytes: 40 << 30,
		Skipped: []string{"npm-cache: emptying the Trash failed, and Finder may still be emptying it"}}
	out := strings.Join(runSummary(res), "\n")
	for _, want := range [][2]string{
		{"Freed now", "30.0 GiB"},
		{"In the Trash", " 0 B"},
		{"In regrow staging", "40.0 GiB"},
		{"skipped:", "npm-cache"},
	} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, want[0]) && strings.Contains(line, want[1]) {
				found = true
			}
		}
		if !found {
			t.Errorf("summary needs a line with %q and %q, got:\n%s", want[0], want[1], out)
		}
	}
}
