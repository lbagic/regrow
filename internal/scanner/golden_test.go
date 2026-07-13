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

// buildFixture creates a fake machine: a home directory plus a fake
// filesystem root (Host.Root) so rules targeting /Library and
// /Applications get golden coverage too. Every path rule in the
// catalog has its target present here.
func buildFixture(t *testing.T) engine.Host {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "Users/dev")

	forHome := []string{
		"Library/Developer/Xcode/DerivedData/MyApp-abc/Build/x.o", // xcode-derived-data
		"Library/Developer/Xcode/iOS DeviceSupport/17.0/dsc",      // xcode-device-support
		"Library/Developer/Xcode/watchOS DeviceSupport/10.0/dsc",
		"Library/Developer/Xcode/Archives/2026/App.xcarchive/Info.plist", // xcode-archives
		"Library/Developer/CoreSimulator/Caches/dyld/blob",               // sim-caches
		"Library/Caches/go-build/00/hash",                                // go-build-cache (+ library-caches)
		"go/pkg/mod/example.com/pkg@v1/go.mod",                           // go-mod-cache
		".npm/_cacache/blob",                                             // npm-cache
		"Library/Caches/Yarn/v6/pkg.tgz",                                 // yarn-cache
		"Library/pnpm/store/v3/files/blob",                               // pnpm-store
		"Library/Caches/pip/wheels/whl",                                  // pip-cache
		".cache/uv/wheels/whl",                                           // uv-cache
		"Library/Caches/Homebrew/bottle.tar.gz",                          // brew-cache
		"workspace/webapp/node_modules/lodash/index.js",                  // node-modules-dirs
		"Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw",       // docker-vm-disk
		"Library/Containers/com.apple.mediaanalysisd/Data/Library/Caches/blob",  // media-analysis-cache
		"Library/Logs/DiagnosticReports/app.crash",                              // user-logs
		"Library/Logs/CrashReporter/app.crash",
		"Library/Metadata/CoreSpotlight/index.spotlightV3/store", // spotlight-index
		".Trash/old-file",                                        // trash-empty
		"Library/Application Support/MobileSync/Backup/UDID/Manifest.db", // ios-backups
		// rust-target-dirs: one real hit, one decoy without marker
		"workspace/proj/target/CACHEDIR.TAG",
		"workspace/proj/target/debug/bin",
		"workspace/decoy/target/notes.txt",
	}
	for _, p := range forHome {
		writeFile(t, filepath.Join(home, p), 1000)
	}

	forRoot := []string{
		"Library/Application Support/com.apple.idleassetsd/Customer/4KSDR240FPS/a.mov", // aerial (Sequoia)
		"Applications/Install macOS Sequoia.app/Contents/Info.plist",                   // macos-installers
		"Library/Updates/092-12345/payload",                                            // library-updates
	}
	for _, p := range forRoot {
		writeFile(t, filepath.Join(root, p), 1000)
	}

	return engine.Host{OS: "darwin", Version: "15.5", Home: home, Root: root}
}

// fixedItems fakes a tool query: golden runs must never exec docker
// or simctl, and results must be machine-independent.
func fixedItems(items ...engine.Item) ToolQuery {
	return func(context.Context) ([]engine.Item, error) { return items, nil }
}

func fakeQueries() map[string]ToolQuery {
	return map[string]ToolQuery{
		"docker-reclaimable": fixedItems(
			engine.Item{Label: "dangling images", Bytes: 7_500_000_000},
			engine.Item{Label: "build cache", Bytes: 512_000_000},
		),
		"docker-volumes": fixedItems(engine.Item{Label: "unused volumes", Bytes: 2_000_000_000}),
		"simctl-devices": fixedItems(
			engine.Item{Label: "iPhone 15 (iOS 17.0)", Arg: "AAA-111", Bytes: 4_000_000_000},
			engine.Item{Label: "iPhone 16 (iOS 18.0)", Arg: "CCC-333", Bytes: 3_000_000_000},
		),
		"simctl-devices-unavailable": fixedItems(
			engine.Item{Label: "iPhone 14 (iOS 16.4)", Arg: "BBB-222", Bytes: 900_000_000},
		),
		"simctl-runtimes": fixedItems(
			engine.Item{Label: "iOS 17.0 (21A328)", Arg: "11111111-2222", Bytes: 7_000_000_000},
		),
		"tm-snapshots": fixedItems(
			engine.Item{Label: "com.apple.TimeMachine.2026-07-13-090000.local"},
		),
	}
}

func renderPlanForGolden(plan engine.Plan, host engine.Host) string {
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, host.Home, "~")
		return strings.ReplaceAll(s, host.Root, "")
	}
	var b strings.Builder
	for _, a := range plan.Actions {
		cmd := make([]string, len(a.Command))
		for i, tok := range a.Command {
			cmd[i] = normalize(tok)
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", a.Kind, a.RuleID, strings.Join(cmd, " "))
	}
	for _, s := range plan.Skipped {
		fmt.Fprintf(&b, "[skip] %s: %s\n", s.RuleID, normalize(s.Reason))
	}
	return b.String()
}

func TestGoldenPlanEmbeddedCatalog(t *testing.T) {
	host := buildFixture(t)

	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	s := New(host)
	s.Queries = fakeQueries()
	findings := s.Scan(context.Background(), catalog)
	plan := engine.BuildPlan(host, findings, nil)
	got := renderPlanForGolden(plan, host)

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
