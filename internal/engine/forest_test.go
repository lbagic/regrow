package engine

import (
	"reflect"
	"testing"
)

func TestForestParents(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "outer"}, Items: []Item{{Path: "/Users/t/p"}, {Path: "/Users/t/p/q/r"}}},
		{Rule: Rule{ID: "mid"}, Items: []Item{{Path: "/Users/t/p/q/"}}},
		// "/Users/t/p-q" sorts between "/Users/t/p" and "/Users/t/p/q"
		// as a bare string; it must not end p's subtree.
		{Rule: Rule{ID: "later"}, Items: []Item{{Path: "/Users/t/p-q"}, {Path: "/Users/t/p/q/r"}}},
		{Rule: Rule{ID: "tool"}, Items: []Item{{Label: "pathless"}}},
	}
	got := buildForest(findings).parent
	want := map[itemRef]itemRef{
		{1, 0}: {0, 0}, // mid inside outer
		{0, 1}: {1, 0}, // nearest container wins, across rules
		{2, 1}: {0, 1}, // equal paths: the later rule is the child
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parents = %v, want %v", got, want)
	}
}

func skipReasons(p Plan) map[string]string {
	out := map[string]string{}
	for _, s := range p.Skipped {
		out[ItemID(s.RuleID, s.ItemKey)] = s.Reason
	}
	return out
}

func actionIDs(p Plan) []string {
	var out []string
	for _, a := range p.Actions {
		out = append(out, ItemID(a.RuleID, a.ItemKey))
	}
	return out
}

func TestBuildPlanSubsumesNestedSelections(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "outer", Risk: RiskCaution}, Items: []Item{{Path: "/Users/t/p", Bytes: 100}}},
		{Rule: Rule{ID: "inner", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/p/q", Bytes: 40}}},
		{Rule: Rule{ID: "per-item-native", Risk: RiskSafe, NativeCommand: Argv{"tool", "rm", "{path}"}},
			Items: []Item{{Path: "/Users/t/p/q/r", Bytes: 5}, {Path: "/Users/t/elsewhere", Bytes: 7}}},
	}
	plan := BuildPlan(testHost, findings, selectRules(findings...))

	if want := []string{"outer/~/p", "per-item-native/~/elsewhere"}; !reflect.DeepEqual(actionIDs(plan), want) {
		t.Fatalf("actions = %v, want %v", actionIDs(plan), want)
	}
	reasons := skipReasons(plan)
	if r := reasons["inner/~/p/q"]; r != "inside outer/~/p, also selected" {
		t.Errorf("inner skip reason = %q", r)
	}
	// The nearest selected ancestor (inner) is skipped too; the reason
	// names the action that actually deletes the item.
	if r := reasons["per-item-native/~/p/q/r"]; r != "inside outer/~/p, also selected" {
		t.Errorf("nested native item skip reason = %q", r)
	}
	if got := plan.TotalBytes(); got != 107 {
		t.Fatalf("TotalBytes = %d, want the union 107 (100 + 7), not the sum 152", got)
	}
}

func TestBuildPlanEqualPathsPlanOnce(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "first", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/x", Bytes: 9}}},
		{Rule: Rule{ID: "second", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/x", Bytes: 9}}},
	}
	plan := BuildPlan(testHost, findings, selectRules(findings...))
	if want := []string{"first/~/x"}; !reflect.DeepEqual(actionIDs(plan), want) {
		t.Fatalf("actions = %v, want %v", actionIDs(plan), want)
	}
	if r := skipReasons(plan)["second/~/x"]; r != "inside first/~/x, also selected" {
		t.Fatalf("second skip reason = %q", r)
	}
	if plan.TotalBytes() != 9 {
		t.Fatalf("TotalBytes = %d, want 9", plan.TotalBytes())
	}
}

func TestBuildPlanWholeRuleCommandCountsOnlyUncoveredItems(t *testing.T) {
	outer := Finding{Rule: Rule{ID: "outer", Risk: RiskCaution}, Items: []Item{{Path: "/Users/t/p", Bytes: 100}}}
	whole := Finding{
		Rule: Rule{ID: "whole", Risk: RiskSafe, NativeCommand: Argv{"tool", "purge"}},
		Items: []Item{
			{Path: "/Users/t/p/r", Bytes: 10},
			{Path: "/Users/t/s", Bytes: 20},
			{Path: "/Users/t/s/own-child", Bytes: 5},
			{Label: "pathless", Bytes: 1},
		},
	}
	plan := BuildPlan(testHost, []Finding{outer, whole}, selectRules(outer, whole))
	if want := []string{"outer/~/p", "whole/"}; !reflect.DeepEqual(actionIDs(plan), want) {
		t.Fatalf("a whole-rule command cannot leave items out, it still runs: actions = %v", actionIDs(plan))
	}
	if got := plan.Actions[1].Bytes; got != 21 {
		t.Fatalf("whole-rule bytes = %d, want 21 (items outside other actions and outside each other)", got)
	}
	if got := plan.Totals(); got.AfterTrash != 100 || got.FreesNow != 21 {
		t.Fatalf("Totals = %+v", got)
	}

	inside := Finding{
		Rule:  Rule{ID: "inside", Risk: RiskSafe, NativeCommand: Argv{"tool", "purge"}},
		Items: []Item{{Path: "/Users/t/p/a", Bytes: 3}, {Path: "/Users/t/p/b", Bytes: 4}},
	}
	plan = BuildPlan(testHost, []Finding{outer, inside}, selectRules(outer, inside))
	if want := []string{"outer/~/p"}; !reflect.DeepEqual(actionIDs(plan), want) {
		t.Fatalf("a whole-rule command entirely inside another action is dropped: actions = %v", actionIDs(plan))
	}
	if r := skipReasons(plan)["inside/"]; r != "inside outer/~/p, also selected" {
		t.Fatalf("skip reason = %q", r)
	}
}

func TestBuildPlanRefusesItemsHoldingSurfaceOnly(t *testing.T) {
	keep := Finding{Rule: Rule{ID: "keep", Risk: RiskSurfaceOnly}, Items: []Item{{Path: "/Users/t/lib/backup", Bytes: 50}}}
	tests := []struct {
		name     string
		findings []Finding
		selected []string
		refused  string
	}{
		{
			name:     "trash item",
			findings: []Finding{{Rule: Rule{ID: "lib", Risk: RiskCaution}, Items: []Item{{Path: "/Users/t/lib", Bytes: 80}}}, keep},
			selected: []string{"lib"},
			refused:  "lib/~/lib",
		},
		{
			name: "whole-rule command",
			findings: []Finding{{Rule: Rule{ID: "lib", Risk: RiskSafe, NativeCommand: Argv{"tool", "purge"}},
				Items: []Item{{Path: "/Users/t/lib", Bytes: 80}}}, keep},
			selected: []string{"lib"},
			refused:  "lib/",
		},
		{
			name: "per-item command",
			findings: []Finding{{Rule: Rule{ID: "lib", Risk: RiskSafe, NativeCommand: Argv{"tool", "rm", "{path}"}},
				Items: []Item{{Path: "/Users/t/lib", Bytes: 80}}}, keep},
			selected: []string{"lib"},
			refused:  "lib/~/lib",
		},
		{
			name: "same path, surface-only rule first",
			findings: []Finding{
				{Rule: Rule{ID: "keep", Risk: RiskSurfaceOnly}, Items: []Item{{Path: "/Users/t/lib/backup", Bytes: 50}}},
				{Rule: Rule{ID: "dup", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/lib/backup", Bytes: 50}}},
			},
			selected: []string{"dup"},
			refused:  "dup/~/lib/backup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel := map[string]bool{}
			for _, id := range tt.selected {
				sel[id] = true
			}
			plan := BuildPlan(testHost, tt.findings, sel)
			if len(plan.Actions) != 0 {
				t.Fatalf("an item holding a surface-only item must never act: %+v", plan.Actions)
			}
			if r := skipReasons(plan)[tt.refused]; r != "contains keep/~/lib/backup, which regrow never deletes" {
				t.Fatalf("skip reason for %s = %q (skips %+v)", tt.refused, r, plan.Skipped)
			}
		})
	}
}

func TestBuildPlanSurfaceOnlyAncestorDoesNotBlock(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "library-caches", Risk: RiskSurfaceOnly}, Items: []Item{{Path: "/Users/t/Library/Caches", Bytes: 70}}},
		{Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
			Items: []Item{{Path: "/Users/t/Library/Caches/go-build", Bytes: 60}}},
	}
	plan := BuildPlan(testHost, findings, map[string]bool{"go-build-cache": true})
	if len(plan.Actions) != 1 || plan.Actions[0].RuleID != "go-build-cache" || plan.Actions[0].Bytes != 60 {
		t.Fatalf("a cache inside a surface-only report must stay cleanable, got %+v", plan)
	}
}

func TestBuildPlanRefusedAncestorDoesNotSubsume(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "lib", Risk: RiskCaution}, Items: []Item{{Path: "/Users/t/lib", Bytes: 80}}},
		{Rule: Rule{ID: "keep", Risk: RiskSurfaceOnly}, Items: []Item{{Path: "/Users/t/lib/keep", Bytes: 50}}},
		{Rule: Rule{ID: "tmp", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/lib/tmp", Bytes: 20}}},
		{Rule: Rule{ID: "unselected", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/other", Bytes: 30}}},
		{Rule: Rule{ID: "under-unselected", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/other/x", Bytes: 10}}},
	}
	plan := BuildPlan(testHost, findings, map[string]bool{"lib": true, "tmp": true, "under-unselected": true})
	if want := []string{"tmp/~/lib/tmp", "under-unselected/~/other/x"}; !reflect.DeepEqual(actionIDs(plan), want) {
		t.Fatalf("only a planned ancestor subsumes: actions = %v, skips %+v", actionIDs(plan), plan.Skipped)
	}
}
