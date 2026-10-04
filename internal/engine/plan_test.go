package engine

import (
	"reflect"
	"strings"
	"testing"
)

var testHost = Host{OS: "darwin", Version: "15.5", Home: "/Users/t"}

// selectRules selects every given finding's rule as a whole.
func selectRules(findings ...Finding) map[string]bool {
	sel := map[string]bool{}
	for _, f := range findings {
		sel[f.Rule.ID] = true
	}
	return sel
}

func TestBuildPlanNativeWholeRule(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
		Items: []Item{
			{Path: "/Users/t/Library/Caches/go-build", Bytes: 100},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 {
		t.Fatalf("want 1 action, got %+v", plan.Actions)
	}
	a := plan.Actions[0]
	if !reflect.DeepEqual(a.Command, []string{"go", "clean", "-cache"}) || a.Kind != ActionNative || a.Bytes != 100 {
		t.Errorf("action wrong: %+v", a)
	}
}

func TestBuildPlanNativePerItemPlaceholders(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "sim-runtimes", Risk: RiskCaution, NativeCommand: Argv{"xcrun", "simctl", "runtime", "delete", "{arg}"}, Sudo: true},
		Items: []Item{
			{Label: "iOS 17.5", Arg: "8A2C", Bytes: 10},
			{Label: "iOS 18.0", Arg: "9B3D", Bytes: 20},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 2 {
		t.Fatalf("want 2 actions, got %+v", plan.Actions)
	}
	want := []string{"sudo", "xcrun", "simctl", "runtime", "delete", "8A2C"}
	if !reflect.DeepEqual(plan.Actions[0].Command, want) {
		t.Errorf("command = %v, want %v", plan.Actions[0].Command, want)
	}
}

func TestBuildPlanCarriesPreAction(t *testing.T) {
	f := Finding{
		Rule: Rule{
			ID: "docker-volumes-named", Risk: RiskCaution,
			NativeCommand: Argv{"docker", "volume", "rm", "{arg}"},
			PreAction:     PreActionVolumeExport,
		},
		Items: []Item{{Label: "dakr_db", Arg: "dakr_db", Bytes: 10}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 || plan.Actions[0].PreAction != PreActionVolumeExport {
		t.Fatalf("action must carry the rule's pre-action, got %+v", plan.Actions)
	}
}

func TestBuildPlanTrashCarriesPreAction(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "agent-scratch", Risk: RiskCaution, PreAction: PreActionAgentScratchRecheck},
		Items: []Item{{Path: "/private/tmp/claude-1000/-Users-t-proj/11111111-1111-4111-8111-111111111111", Bytes: 10}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionTrash || plan.Actions[0].PreAction != PreActionAgentScratchRecheck {
		t.Fatalf("a Trash move must carry the rule's pre-action, got %+v", plan.Actions)
	}
}

func TestBuildPlanTrashFallback(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "xcode-derived-data", Risk: RiskSafe},
		Items: []Item{
			{Path: "/Users/t/Library/Developer/Xcode/DerivedData", Bytes: 42},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 {
		t.Fatalf("want 1 action, got %+v", plan.Actions)
	}
	a := plan.Actions[0]
	if a.Kind != ActionTrash || a.Command[0] != "osascript" {
		t.Errorf("trash action wrong: %+v", a)
	}
	if !strings.Contains(a.Command[2], "/Users/t/Library/Developer/Xcode/DerivedData") {
		t.Errorf("command misses path: %v", a.Command)
	}
}

func TestBuildPlanSurfaceOnlyNeverActs(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "ios-backups", Risk: RiskSurfaceOnly},
		Items: []Item{{Path: "/Users/t/Library/Application Support/MobileSync/Backup/x", Bytes: 1 << 30}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 0 {
		t.Fatalf("surface-only produced actions: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "surface-only") {
		t.Fatalf("want surface-only skip, got %+v", plan.Skipped)
	}
}

func TestBuildPlanGuardRejectsDangerousPaths(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "broken-rule", Risk: RiskSafe},
		Items: []Item{{Path: "/Users/t", Bytes: 1}}, // home itself
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 0 {
		t.Fatalf("guard let home through: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "path guard") {
		t.Fatalf("want guard skip, got %+v", plan.Skipped)
	}
}

func TestBuildPlanSelection(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "wanted", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/a/b", Bytes: 1}}},
		{Rule: Rule{ID: "unwanted", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/c/d", Bytes: 2}}},
	}
	plan := BuildPlan(testHost, findings, map[string]bool{"wanted": true})
	if len(plan.Actions) != 1 || plan.Actions[0].RuleID != "wanted" {
		t.Fatalf("selection ignored: %+v", plan.Actions)
	}
}

func TestBuildPlanPathlessItemWithoutNativeCommand(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "odd", Risk: RiskSafe},
		Items: []Item{{Label: "ghost", Bytes: 5}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("pathless item mishandled: %+v", plan)
	}
}

func TestPlanTotalBytes(t *testing.T) {
	p := Plan{Actions: []Action{{Bytes: 3}, {Bytes: 4}}}
	if p.TotalBytes() != 7 {
		t.Fatalf("TotalBytes = %d", p.TotalBytes())
	}
}

func TestBuildPlanItemAtomSubset(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "sim-devices", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"xcrun", "simctl", "delete", "{arg}"}},
		Items: []Item{
			{Label: "iPhone 15", Arg: "AAA-111", Key: "AAA-111", Bytes: 10},
			{Label: "iPhone 16", Arg: "CCC-333", Key: "CCC-333", Bytes: 20},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"sim-devices/CCC-333": true})
	if len(plan.Actions) != 1 || plan.Actions[0].ItemKey != "CCC-333" {
		t.Fatalf("item atom should plan exactly one item, got %+v", plan.Actions)
	}
	if plan.Actions[0].Command[len(plan.Actions[0].Command)-1] != "CCC-333" {
		t.Errorf("command targets wrong item: %v", plan.Actions[0].Command)
	}
	if len(plan.Unmatched) != 0 {
		t.Errorf("matched atom reported unmatched: %v", plan.Unmatched)
	}
}

func TestBuildPlanItemAtomDerivesMissingKeys(t *testing.T) {
	// Hand-built findings without keys must still match item atoms.
	f := Finding{
		Rule:  Rule{ID: "xcode-archives", Risk: RiskCaution},
		Items: []Item{{Path: "/Users/t/Library/Archives/A.xcarchive", Bytes: 1}, {Path: "/Users/t/Library/Archives/B.xcarchive", Bytes: 2}},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"xcode-archives/~/Library/Archives/B.xcarchive": true})
	if len(plan.Actions) != 1 || plan.Actions[0].Path != "/Users/t/Library/Archives/B.xcarchive" {
		t.Fatalf("tilde-key atom should match, got %+v", plan.Actions)
	}
	if plan.Actions[0].ItemKey != "~/Library/Archives/B.xcarchive" {
		t.Errorf("action must carry the item key, got %q", plan.Actions[0].ItemKey)
	}
}

func TestBuildPlanWholeRuleCommandRefusesPartialSelection(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
		Items: []Item{
			{Path: "/Users/t/Library/Caches/go-build", Key: "~/Library/Caches/go-build", Bytes: 100},
			{Path: "/Users/t/other/go-build", Key: "~/other/go-build", Bytes: 50},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"go-build-cache/~/other/go-build": true})
	if len(plan.Actions) != 0 {
		t.Fatalf("partial selection on a whole-rule command must never act: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "whole-rule command") {
		t.Fatalf("want whole-rule skip with reason, got %+v", plan.Skipped)
	}

	// Selecting every item extensionally equals the whole rule.
	full := BuildPlan(testHost, []Finding{f}, map[string]bool{
		"go-build-cache/~/Library/Caches/go-build": true,
		"go-build-cache/~/other/go-build":          true,
	})
	if len(full.Actions) != 1 || full.Actions[0].Bytes != 150 || full.Actions[0].ItemKey != "" {
		t.Fatalf("all-item atoms should plan the whole command once, got %+v", full.Actions)
	}
}

func TestBuildPlanUnmatchedSelectors(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "sim-devices", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"xcrun", "simctl", "delete", "{arg}"}},
			Items: []Item{{Label: "iPhone", Arg: "AAA-111", Key: "AAA-111", Bytes: 1}}},
		{Rule: Rule{ID: "empty-rule", Risk: RiskSafe}},
	}
	plan := BuildPlan(testHost, findings, map[string]bool{
		"sim-devices/AAA-111": true,
		"sim-devices/TYPO":    true,
		"no-such-rule":        true,
		"empty-rule":          true, // rule exists, found nothing: not a typo
	})
	want := []string{"no-such-rule", "sim-devices/TYPO"}
	if !reflect.DeepEqual(plan.Unmatched, want) {
		t.Fatalf("Unmatched = %v, want %v", plan.Unmatched, want)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("valid atom must still plan, got %+v", plan.Actions)
	}
}

func TestBuildPlanSurfaceOnlyItemAtomStillSkips(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "ios-backups", Risk: RiskSurfaceOnly},
		Items: []Item{{Path: "/Users/t/Library/Backup/x", Key: "~/Library/Backup/x", Bytes: 1}},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"ios-backups/~/Library/Backup/x": true})
	if len(plan.Actions) != 0 {
		t.Fatalf("surface-only item atom produced actions: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "surface-only") {
		t.Fatalf("want surface-only skip, got %+v", plan.Skipped)
	}
}

func TestBuildPlanSkipsItemWithEmptyPlaceholderValue(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "sim-runtimes", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"xcrun", "simctl", "runtime", "delete", "{arg}"}},
		Items: []Item{
			{Label: "good", Arg: "8A2C", Bytes: 10},
			{Label: "bad-no-arg", Bytes: 20},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 || plan.Actions[0].Command[len(plan.Actions[0].Command)-1] != "8A2C" {
		t.Fatalf("want 1 action for the good item, got %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "{arg}") {
		t.Fatalf("empty placeholder value must be a skip with reason, got %+v", plan.Skipped)
	}
}

func TestBuildPlanEmptySelectionPlansNothing(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "safe-cache", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/cache", Bytes: 10}}},
		{Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}}, Items: []Item{{Path: "/Users/t/gb", Bytes: 20}}},
	}
	for name, sel := range map[string]map[string]bool{
		"nil":       nil,
		"empty":     {},
		"all false": {"safe-cache": false, "go-build-cache/~/gb": false},
	} {
		t.Run(name, func(t *testing.T) {
			plan := BuildPlan(testHost, findings, sel)
			if len(plan.Actions) != 0 || len(plan.Skipped) != 0 || len(plan.Unmatched) != 0 {
				t.Fatalf("an empty selection must plan nothing, got %+v", plan)
			}
		})
	}
}

func TestDefaultSelection(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "safe-found", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/a", Bytes: 1}}},
		{Rule: Rule{ID: "safe-empty", Risk: RiskSafe}},
		{Rule: Rule{ID: "safe-errored", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/b", Bytes: 1}}, Err: "partly unreadable"},
		{Rule: Rule{ID: "caution-found", Risk: RiskCaution}, Items: []Item{{Path: "/Users/t/c", Bytes: 1}}},
		{Rule: Rule{ID: "surface-found", Risk: RiskSurfaceOnly}, Items: []Item{{Path: "/Users/t/d", Bytes: 1}}},
	}
	got := DefaultSelection(findings)
	want := map[string]bool{"safe-found": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultSelection = %v, want %v", got, want)
	}
}

func TestBuildPlanEmptiesTrashFirst(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "cache-a", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/a", Bytes: 1}}},
		{Rule: Rule{ID: "empty-the-trash", Risk: RiskCaution, NativeCommand: Argv{"empty-trash"}, EmptiesTrash: true},
			Items: []Item{{Path: "/Users/t/.Trash", Bytes: 5}}},
		{Rule: Rule{ID: "cache-b", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/b", Bytes: 1}}},
	}
	plan := BuildPlan(testHost, findings, selectRules(findings...))
	var order []string
	for _, a := range plan.Actions {
		order = append(order, a.RuleID)
	}
	if want := []string{"empty-the-trash", "cache-a", "cache-b"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("action order = %v, want %v (Trash emptied before this run's moves, the rest in catalog order)", order, want)
	}
}

// The embedded catalog's trash-empty must carry empties_trash: a plan
// over every actionable rule runs it before anything else.
func TestCatalogPlanRunsTrashEmptyFirst(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var findings []Finding
	sel := map[string]bool{}
	for _, r := range catalog {
		if !r.Risk.Actionable() {
			continue
		}
		findings = append(findings, Finding{Rule: r, Items: []Item{{
			Path:  "/Users/t/fixture/" + r.ID,
			Arg:   "arg-" + r.ID,
			Label: r.ID,
			Bytes: 1,
		}}})
		sel[r.ID] = true
	}
	plan := BuildPlan(testHost, findings, sel)
	if len(plan.Actions) < 2 {
		t.Fatalf("want a multi-rule plan, got %+v", plan)
	}
	if plan.Actions[0].RuleID != "trash-empty" {
		t.Fatalf("first action = %s, want trash-empty", plan.Actions[0].RuleID)
	}
	for _, a := range plan.Actions[1:] {
		if a.RuleID == "trash-empty" {
			t.Fatal("trash-empty planned twice")
		}
	}
}

func TestPlanTotalsSplitByKind(t *testing.T) {
	p := Plan{Actions: []Action{
		{Kind: ActionTrash, Bytes: 100},
		{Kind: ActionNative, Bytes: 20},
		{Kind: ActionTrash, Bytes: 3},
	}}
	got := p.Totals()
	if got.FreesNow != 20 || got.AfterTrash != 103 {
		t.Fatalf("Totals = %+v, want FreesNow 20, AfterTrash 103", got)
	}
}
