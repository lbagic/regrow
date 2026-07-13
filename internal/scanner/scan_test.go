package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func TestScanStaticPaths(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "Library", "Caches", "go-build", "obj.o"), 10_000)

	host := engine.Host{OS: "darwin", Version: "15.5", Home: home}
	rules := []engine.Rule{
		{
			ID: "go-build-cache", Title: "Go build cache", Category: "dev-caches", Risk: engine.RiskSafe,
			Paths: map[string][]engine.PathEntry{"darwin": {{Path: "~/Library/Caches/go-build"}}},
		},
		{
			ID: "absent-rule", Title: "Absent", Category: "dev-caches", Risk: engine.RiskSafe,
			Paths: map[string][]engine.PathEntry{"darwin": {{Path: "~/Library/Caches/never-there"}}},
		},
	}
	findings := New(host).Scan(context.Background(), rules)
	if len(findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(findings))
	}
	if findings[0].Rule.ID != "go-build-cache" || len(findings[0].Items) != 1 {
		t.Fatalf("first finding wrong: %+v", findings[0])
	}
	if findings[0].Items[0].Bytes < 10_000 {
		t.Errorf("measured %d bytes, want >= 10000", findings[0].Items[0].Bytes)
	}
	if findings[0].Items[0].LastUsed.IsZero() {
		t.Error("LastUsed not set")
	}
	if findings[0].Items[0].Key != "~/Library/Caches/go-build" {
		t.Errorf("path item key = %q, want tilde-abbreviated path", findings[0].Items[0].Key)
	}
	if len(findings[1].Items) != 0 || findings[1].Err != "" {
		t.Errorf("absent path must yield empty finding without error: %+v", findings[1])
	}
}

func TestScanToolQuery(t *testing.T) {
	host := engine.Host{OS: "darwin", Home: t.TempDir()}
	rule := engine.Rule{
		ID: "fake-tool", Title: "Fake", Category: "containers", Risk: engine.RiskSafe,
		ToolQuery: "fake", NativeCommand: engine.Argv{"fake", "rm", "{arg}"},
	}

	s := New(host)
	s.Queries["fake"] = func(ctx context.Context) ([]engine.Item, error) {
		return []engine.Item{{Label: "image:latest", Arg: "image:latest", Bytes: 500}}, nil
	}
	findings := s.Scan(context.Background(), []engine.Rule{rule})
	if len(findings[0].Items) != 1 || findings[0].Items[0].Bytes != 500 {
		t.Fatalf("tool items wrong: %+v", findings[0])
	}
	if findings[0].Items[0].Key != "image:latest" {
		t.Errorf("tool item key = %q, want the arg", findings[0].Items[0].Key)
	}

	s.Queries["fake"] = func(ctx context.Context) ([]engine.Item, error) {
		return nil, errors.New("docker daemon not running")
	}
	findings = s.Scan(context.Background(), []engine.Rule{rule})
	if findings[0].Err == "" || !strings.Contains(findings[0].Err, "daemon") {
		t.Fatalf("tool error not surfaced: %+v", findings[0])
	}
}

func TestScanUnknownToolQuery(t *testing.T) {
	host := engine.Host{OS: "darwin", Home: t.TempDir()}
	rule := engine.Rule{
		ID: "bad-tool", Title: "Bad", Category: "x", Risk: engine.RiskSafe, ToolQuery: "nope",
	}
	findings := New(host).Scan(context.Background(), []engine.Rule{rule})
	if !strings.Contains(findings[0].Err, `unknown tool query "nope"`) {
		t.Fatalf("want unknown-query error, got %+v", findings[0])
	}
}
