package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/aerials"
	"github.com/lbagic/regrow/internal/engine"
)

// heldRule is a caution rule held by a check the test controls.
func heldRule() engine.Rule {
	return engine.Rule{ID: "held", Title: "Held", Category: "macos", Risk: engine.RiskCaution,
		ToolQuery: "one", Hold: "test-hold", PreAction: engine.PreActionHoldRecheck, Note: "the rule's own note"}
}

func heldScanner(t *testing.T, why string, err error) *Scanner {
	t.Helper()
	return &Scanner{
		Host: engine.Host{OS: "darwin", Version: "15.7.7", Home: t.TempDir()},
		Queries: map[string]ToolQuery{"one": func(context.Context) ([]engine.Item, error) {
			return []engine.Item{{Label: "a", Key: "a", Bytes: 1 << 30}}, nil
		}},
		Holds: map[string]HoldCheck{"test-hold": func(context.Context) (string, error) { return why, err }},
	}
}

func TestAHeldFindingIsShownOnly(t *testing.T) {
	f := heldScanner(t, "Kept while the switch is on.", nil).Scan(context.Background(), []engine.Rule{heldRule()})[0]
	if f.Held != "Kept while the switch is on." || f.Rule.Risk != engine.RiskSurfaceOnly || f.Rule.Note != f.Held || len(f.Items) != 1 {
		t.Fatalf("finding = %+v, want it surface-only with the reason as its note", f)
	}
	if sel := engine.Selectable(f); sel != nil {
		t.Errorf("atoms %v for a held finding", sel)
	}
	plan := engine.BuildPlan(engine.Host{}, []engine.Finding{f}, map[string]bool{"held": true})
	if len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != f.Held {
		t.Errorf("plan = %+v, want one skip naming the hold", plan)
	}
	if b := engine.Bucket(f.Rule); b != engine.BucketShownOnly {
		t.Errorf("bucket = %q, want shown only", b)
	}

	free := heldScanner(t, "", nil).Scan(context.Background(), []engine.Rule{heldRule()})[0]
	if free.Held != "" || free.Rule.Risk != engine.RiskCaution || free.Rule.Note != "the rule's own note" {
		t.Errorf("finding with nothing holding it = %+v", free)
	}

	failed := heldScanner(t, "", errors.New("no answer")).Scan(context.Background(), []engine.Rule{heldRule()})[0]
	if !strings.Contains(failed.Held, "no answer") || failed.Rule.Risk != engine.RiskSurfaceOnly {
		t.Errorf("finding whose hold failed = %+v, want it kept", failed)
	}
}

// aerialsFixture makes Aerial.sqlite under root with picks rows picked.
func aerialsFixture(t *testing.T, root string, picks int) {
	t.Helper()
	db := filepath.Join(root, aerials.DB)
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	sql := `CREATE TABLE ZUSER (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE ZCATEGORY (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE ZSUBCATEGORY (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE ZASSET (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER VARCHAR);
CREATE TABLE Z_2ACTIVEFORUSERS (Z_2ACTIVECATEGORIES INTEGER, Z_4ACTIVEFORUSERS INTEGER);
CREATE TABLE Z_3ACTIVEFORUSERS (Z_3ACTIVESUBCATEGORIES INTEGER, Z_4ACTIVEFORUSERS1 INTEGER);
CREATE TABLE Z_1PINNEDFORUSERS (Z_1PINNEDASSETS INTEGER, Z_4PINNEDFORUSERS INTEGER);`
	for i := 1; i <= picks; i++ {
		sql += "INSERT INTO Z_2ACTIVEFORUSERS VALUES (" + string(rune('0'+i)) + ", 1);"
	}
	if out, err := exec.Command("/usr/bin/sqlite3", "-bail", db, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
}

func TestAerialPicksHold(t *testing.T) {
	root := t.TempDir()
	host := engine.Host{OS: "darwin", Version: "15.7.7", Root: root}
	if why, err := aerialPicksHold(context.Background(), host); why != "" || err != nil {
		t.Errorf("no database: %q, %v; want nothing held", why, err)
	}
	aerialsFixture(t, root, 2)
	why, _ := aerialPicksHold(context.Background(), host)
	if !strings.Contains(why, "2 categories") || !strings.Contains(why, "`regrow aerials off`") {
		t.Errorf("picks on: %q", why)
	}
	rule := engine.Rule{ID: "aerial-wallpapers", Hold: engine.HoldAerialPicks}
	if err := RecheckHold(context.Background(), host, rule); err == nil || err.Error() != why {
		t.Errorf("recheck with picks on: %v, want %q", err, why)
	}
	host.Version = "26.0"
	if why, _ := aerialPicksHold(context.Background(), host); !strings.Contains(why, "macOS 26.0") {
		t.Errorf("macOS 26: %q, want it kept", why)
	}

	clear := t.TempDir()
	aerialsFixture(t, clear, 0)
	if err := RecheckHold(context.Background(), engine.Host{OS: "darwin", Version: "15.7.7", Root: clear}, rule); err != nil {
		t.Errorf("recheck with nothing picked: %v", err)
	}
}
