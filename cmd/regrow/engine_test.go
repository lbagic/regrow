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
		Finding   *engine.Finding  `json:"finding"`
		Plan      *engine.Plan     `json:"plan"`
		Exclusive map[string]int64 `json:"exclusive"`
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
	if exclusive := next("summary").Exclusive; exclusive["fixture-cache/~/proj/cache"] < 8192 {
		t.Fatalf("summary exclusive = %v, want the ledger of the scan", exclusive)
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

// `regrow scan --json` is the engine's scan without hello: a temp home
// with a readable cache, a cache with a folder it may not open, and a
// folder it may not list at all.
func TestScanJSONIsTheEngineScanWithoutHello(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	home := t.TempDir()
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 8192), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lock := func(rel string) {
		t.Helper()
		p := filepath.Join(home, rel)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	}
	write("read/a.bin")
	write("part/a.bin")
	write("part/locked/b.bin")
	write("gone/c.bin")
	lock("part/locked")
	lock("gone")
	rule := func(id, path string) engine.Rule {
		return engine.Rule{ID: id, Title: id, Category: "fixture", Risk: engine.RiskSafe,
			Paths: map[string][]engine.PathEntry{"darwin": {{Path: path}}}}
	}
	catalog := []engine.Rule{rule("fixture-read", "~/read"), rule("fixture-part", "~/part"), rule("fixture-gone", "~/gone")}
	host := engine.Host{OS: "darwin", Version: "15.5", Home: home}

	var out strings.Builder
	newEngineServer(host, catalog, nil).ScanOnce(context.Background(), &out)

	type item struct {
		Bytes   int64 `json:"bytes"`
		Partial bool  `json:"partial"`
	}
	type line struct {
		Event   string          `json:"event"`
		Re      *string         `json:"re"`
		TookMS  *int64          `json:"took_ms"`
		Finding *struct {
			Rule  struct{ ID string } `json:"rule"`
			Items []item              `json:"items"`
		} `json:"finding"`
		Totals *engine.Totals `json:"totals"`
	}
	var events []string
	items := map[string]item{}
	var totals *engine.Totals
	for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("not an event line: %q", raw)
		}
		if l.Re != nil {
			t.Errorf("a line answers no request, so it has no re: %s", raw)
		}
		events = append(events, l.Event)
		switch l.Event {
		case "finding":
			if l.TookMS == nil || len(l.Finding.Items) != 1 {
				t.Fatalf("finding = %s, want took_ms and one item", raw)
			}
			items[l.Finding.Rule.ID] = l.Finding.Items[0]
		case "summary":
			totals = l.Totals
		}
	}
	if got := strings.Join(events, ","); got != "start,finding,finding,finding,summary,done" {
		t.Fatalf("events = %s", got)
	}
	if it := items["fixture-read"]; it.Partial || it.Bytes < 8192 {
		t.Errorf("readable item = %+v", it)
	}
	if it := items["fixture-part"]; !it.Partial || it.Bytes < 8192 {
		t.Errorf("partly readable item = %+v, want a partial lower bound", it)
	}
	if it := items["fixture-gone"]; !it.Partial || it.Bytes != 0 {
		t.Errorf("unlistable item = %+v, want partial with nothing measured", it)
	}
	if totals == nil || totals.Partial != 2 || totals.AfterTrash < 2*8192 {
		t.Errorf("summary totals = %+v", totals)
	}
}
