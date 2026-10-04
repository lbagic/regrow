package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// The engine wiring, scan and plan only: a real execute would hand the
// fixture to the real Finder. The catalog is one synthetic trash-only
// rule in a temp home.
func TestEngineScansAndPlansThroughTheRealScanner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	target := filepath.Join(home, "proj", "cache")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.bin"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	host := engine.Host{OS: "darwin", Version: "15.5", Home: home}
	catalog := []engine.Rule{{
		ID: "fixture-cache", Title: "Fixture cache", Category: "fixture", Risk: engine.RiskSafe,
		Paths: map[string][]engine.PathEntry{"darwin": {{Path: "~/proj/cache"}}},
	}}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- serveEngine(context.Background(), host, catalog, inR, outW)
		_ = outW.Close()
	}()
	lines := bufio.NewScanner(outR)
	type event struct {
		Event   string          `json:"event"`
		Re      string          `json:"re"`
		Version string          `json:"version"`
		ScanID  string          `json:"scan_id"`
		Finding *engine.Finding `json:"finding"`
		Plan    *engine.Plan    `json:"plan"`
	}
	next := func(want string) event {
		t.Helper()
		if !lines.Scan() {
			t.Fatalf("the engine closed its output, want %s", want)
		}
		var e event
		if err := json.Unmarshal(lines.Bytes(), &e); err != nil || e.Event != want {
			t.Fatalf("got line %s, want event %s (%v)", lines.Text(), want, err)
		}
		return e
	}
	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}

	if hello := next("hello"); hello.Version != version {
		t.Fatalf("hello announced version %q, want %q", hello.Version, version)
	}
	send(`{"type":"scan","id":"s"}`)
	scanID := next("start").ScanID
	found := next("finding").Finding
	if found.Rule.ID != "fixture-cache" || len(found.Items) != 1 {
		t.Fatalf("finding = %+v", found)
	}
	if it := found.Items[0]; it.Path != target || it.Key != "~/proj/cache" || it.Bytes < 8192 {
		t.Fatalf("item = %+v, want the measured fixture directory", it)
	}
	next("done")

	send(`{"type":"plan","id":"p","scan_id":"` + scanID + `"}`)
	plan := next("plan").Plan
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != engine.ActionTrash || plan.Actions[0].Path != target {
		t.Fatalf("plan = %+v, want one Trash move of the fixture", plan)
	}
	next("done")

	_ = inW.Close()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "a.bin")); err != nil {
		t.Fatalf("scan and plan must not touch the fixture: %v", err)
	}
}
