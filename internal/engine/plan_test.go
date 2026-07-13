package engine

import (
	"reflect"
	"strings"
	"testing"
)

var testHost = Host{OS: "darwin", Version: "15.5", Home: "/Users/t"}

func TestBuildPlanNativeWholeRule(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
		Items: []Item{
			{Path: "/Users/t/Library/Caches/go-build", Bytes: 100},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, nil)
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
	plan := BuildPlan(testHost, []Finding{f}, nil)
	if len(plan.Actions) != 2 {
		t.Fatalf("want 2 actions, got %+v", plan.Actions)
	}
	want := []string{"sudo", "xcrun", "simctl", "runtime", "delete", "8A2C"}
	if !reflect.DeepEqual(plan.Actions[0].Command, want) {
		t.Errorf("command = %v, want %v", plan.Actions[0].Command, want)
	}
}

func TestBuildPlanTrashFallback(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "xcode-derived-data", Risk: RiskSafe},
		Items: []Item{
			{Path: "/Users/t/Library/Developer/Xcode/DerivedData", Bytes: 42},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, nil)
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
	plan := BuildPlan(testHost, []Finding{f}, nil)
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
	plan := BuildPlan(testHost, []Finding{f}, nil)
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
	plan := BuildPlan(testHost, []Finding{f}, nil)
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
