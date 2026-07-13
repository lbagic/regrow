package scanner

// The golden harness runs the real embedded catalog against a fixture
// $HOME and snapshots the dry-run command list — the contract PLAN.md
// Prompt C promises and every future rule extends (CLAUDE.md: every
// rule gets a golden test). Sizes and temp paths are normalized out;
// the command list is the artifact. Refresh with:
//
//	go test ./internal/scanner -run Golden -update

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

var update = flag.Bool("update", false, "rewrite golden files")

func buildFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	// xcode-derived-data
	writeFile(t, filepath.Join(home, "Library/Developer/Xcode/DerivedData/MyApp-abc/Build/x.o"), 1000)
	// go-build-cache
	writeFile(t, filepath.Join(home, "Library/Caches/go-build/00/hash"), 1000)
	// rust-target-dirs: one real hit, one decoy without marker
	writeFile(t, filepath.Join(home, "workspace/proj/target/CACHEDIR.TAG"), 10)
	writeFile(t, filepath.Join(home, "workspace/proj/target/debug/bin"), 1000)
	writeFile(t, filepath.Join(home, "workspace/decoy/target/notes.txt"), 10)
	return home
}

func renderPlanForGolden(plan engine.Plan, home string) string {
	var b strings.Builder
	for _, a := range plan.Actions {
		cmd := make([]string, len(a.Command))
		for i, tok := range a.Command {
			cmd[i] = strings.ReplaceAll(tok, home, "~")
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", a.Kind, a.RuleID, strings.Join(cmd, " "))
	}
	for _, s := range plan.Skipped {
		fmt.Fprintf(&b, "[skip] %s: %s\n", s.RuleID, strings.ReplaceAll(s.Reason, home, "~"))
	}
	return b.String()
}

func TestGoldenPlanEmbeddedCatalog(t *testing.T) {
	home := buildFixtureHome(t)
	host := engine.Host{OS: "darwin", Version: "15.5", Home: home}

	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	findings := New(host).Scan(context.Background(), catalog)
	plan := engine.BuildPlan(host, findings, nil)
	got := renderPlanForGolden(plan, home)

	goldenPath := filepath.Join("testdata", "golden", "plan.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("plan drifted from golden.\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}
