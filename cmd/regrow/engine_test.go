package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
		served <- serveEngine(context.Background(), host, catalog, nil, inR, outW)
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

func TestCleanFlagsLoadTheSameCatalogFromTerminal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := cleanFlags(options{rulesDir: "my-rules", betaRules: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--rules-dir", filepath.Join(dir, "my-rules"), "--beta-rules"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cleanFlags = %q, want %q", got, want)
	}
	if got, _ := cleanFlags(options{}); len(got) != 0 {
		t.Fatalf("the embedded catalog needs no flags, got %q", got)
	}
}

// A stand-in docker on PATH: the export's reason must reach the error
// the journal records, and its stdout the tarball.
func TestDockerStreamCapturesTheExportsStderr(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf tar-bytes\necho \"volume busy: $*\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var tarball strings.Builder
	err := dockerStream(context.Background(), []string{"run", "--rm", "fixture-volume"}, &tarball)
	if tarball.String() != "tar-bytes" {
		t.Errorf("tarball got %q", tarball.String())
	}
	if err == nil || !strings.Contains(err.Error(), "volume busy: run --rm fixture-volume") {
		t.Fatalf("err = %v, want docker's stderr in it", err)
	}
}

func TestStewardCommandsKeepTheDefaultSIGPIPE(t *testing.T) {
	keepRunningOnEPIPE()
	t.Cleanup(func() { signal.Reset(syscall.SIGPIPE) })
	// yes dies quietly of SIGPIPE when head exits; with SIGPIPE ignored
	// it gets EPIPE and complains on stderr.
	var stderr strings.Builder
	cmd := exec.Command("/bin/sh", "-c", "yes | head -1")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "" {
		t.Fatalf("a child inherited an ignored SIGPIPE: %q", stderr.String())
	}
}
