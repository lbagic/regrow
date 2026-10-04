package scanner

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

const testStall = 250 * time.Millisecond

// blockingWalker opens the given directories the way a TCC-gated
// container does without Full Disk Access: the call never returns. It
// counts the opens of each blocked path. Blocked calls are released at
// cleanup, which proves a late answer is discarded rather than racing
// the finished scan.
func blockingWalker(t *testing.T, block ...string) (*walker, *atomic.Int32) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return blockingWalkerUntil(release, block...)
}

func blockingWalkerUntil(release <-chan struct{}, block ...string) (*walker, *atomic.Int32) {
	var opens atomic.Int32
	w := &walker{
		open: func(p string) (dirHandle, error) {
			if slices.Contains(block, p) {
				opens.Add(1)
				<-release
			}
			return openDir(p)
		},
		stall:   testStall,
		workers: walkWorkers,
		blocked: &blockedSet{},
	}
	return w, &opens
}

func pathRule(id string, paths ...string) engine.Rule {
	entries := make([]engine.PathEntry, len(paths))
	for i, p := range paths {
		entries[i] = engine.PathEntry{Path: p}
	}
	return engine.Rule{
		ID: id, Title: id, Category: "test", Risk: engine.RiskSafe,
		Paths: map[string][]engine.PathEntry{"darwin": entries},
	}
}

// scanWithin runs one scan and fails the test if it does not return
// well inside the bound a blocked directory may cost.
func scanWithin(t *testing.T, s *Scanner, rules ...engine.Rule) []engine.Finding {
	t.Helper()
	done := make(chan []engine.Finding, 1)
	go func() { done <- s.Scan(context.Background(), rules) }()
	select {
	case f := <-done:
		return f
	case <-time.After(30 * testStall):
		t.Fatal("scan did not return: a blocked directory hung it")
		return nil
	}
}

// fixtureTree plants a tree whose blocked folder sits three levels
// below the measured root (depth >= 2), beside readable siblings.
func fixtureTree(t *testing.T) (home, root, blocked string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(home, "cache")
	writeFile(t, filepath.Join(root, "top.bin"), 10_000)
	writeFile(t, filepath.Join(root, "a", "mid.bin"), 20_000)
	blocked = filepath.Join(root, "a", "b", "gated")
	writeFile(t, filepath.Join(blocked, "hidden.bin"), 500_000)
	writeFile(t, filepath.Join(root, "c", "d", "deep.bin"), 30_000)
	return home, root, blocked
}

func TestScanReturnsWhenADeepFolderBlocks(t *testing.T) {
	home, root, blocked := fixtureTree(t)
	w, opens := blockingWalker(t, blocked)
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: w}

	f := scanWithin(t, s, pathRule("cache", "~/cache"))[0]
	if f.Err != "" || len(f.Items) != 1 {
		t.Fatalf("finding = %+v, want one item and no rule error", f)
	}
	it := f.Items[0]
	if it.Path != root || !it.Partial {
		t.Fatalf("item = %+v, want the root marked Partial", it)
	}
	if it.Bytes < 60_000 || it.Bytes >= 500_000 {
		t.Errorf("bytes = %d, want every readable file (>= 60000) and nothing behind the blocked folder", it.Bytes)
	}

	// A rescan in the same process never opens the blocked folder
	// again: one stuck goroutine per path, not one per scan.
	f = scanWithin(t, s, pathRule("cache", "~/cache"))[0]
	if !f.Items[0].Partial {
		t.Error("rescan must still report the item as partial")
	}
	if n := opens.Load(); n != 1 {
		t.Errorf("blocked folder opened %d times, want 1", n)
	}
}

func TestScanReturnsWhenAGlobParentBlocks(t *testing.T) {
	home := t.TempDir()
	models := filepath.Join(home, "models")
	writeFile(t, filepath.Join(models, "a.gguf"), 1_000)
	other := filepath.Join(home, "other.bin")
	writeFile(t, other, 2_000)
	w, _ := blockingWalker(t, models)
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: w}

	f := scanWithin(t, s, pathRule("models", "~/models/*.gguf", "~/other.bin"))[0]
	if f.Err != "" {
		t.Fatalf("a blocked glob parent is not a rule failure, got Err %q", f.Err)
	}
	var marker, measured *engine.Item
	for i := range f.Items {
		switch f.Items[i].Path {
		case "":
			marker = &f.Items[i]
		case other:
			measured = &f.Items[i]
		}
	}
	if marker == nil || !marker.Partial || !strings.Contains(marker.Label, "~/models/*.gguf") {
		t.Fatalf("items = %+v, want a pathless Partial marker naming the pattern", f.Items)
	}
	if measured == nil || measured.Partial {
		t.Fatalf("items = %+v, want the rule's other path measured in full", f.Items)
	}
	if len(f.Items) != 2 {
		t.Fatalf("items = %+v, want the marker and the readable path only", f.Items)
	}
}

func TestDiscoverReturnsWhenAFolderBlocks(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "workspace", "app", "node_modules", "x", "index.js"))
	gated := filepath.Join(home, "workspace", "client", "gated")
	touch(t, filepath.Join(gated, "web", "node_modules", "y", "index.js"))
	w, _ := blockingWalker(t, gated)
	host := engine.Host{OS: "darwin", Home: home}

	done := make(chan struct{})
	var hits, partial []string
	go func() {
		defer close(done)
		hits, partial = discover(context.Background(), w, host, engine.Discover{Roots: []string{"~/workspace"}, Name: "node_modules", MaxDepth: 10})
	}()
	select {
	case <-done:
	case <-time.After(30 * testStall):
		t.Fatal("discover did not return")
	}
	if want := []string{filepath.Join(home, "workspace", "app", "node_modules")}; !slices.Equal(hits, want) {
		t.Errorf("hits = %v, want %v", hits, want)
	}
	if want := []string{filepath.Join(home, "workspace")}; !slices.Equal(partial, want) {
		t.Errorf("partial roots = %v, want %v", partial, want)
	}
}

func TestHFAndOllamaWalkersReturnWhenAFolderBlocks(t *testing.T) {
	hub := t.TempDir()
	fakeHFRepo(t, hub, "models--org--open", 1)
	fakeHFRepo(t, hub, "models--org--gated", 1)
	gatedBlobs := filepath.Join(hub, "models--org--gated", "blobs")

	manifests := t.TempDir()
	writeManifest(t, manifests, []string{"registry.ollama.ai", "library", "llama3", "latest"},
		`{"config":{"digest":"sha256-c1","size":5},"layers":[{"digest":"sha256-a","size":10}]}`)
	gatedRegistry := filepath.Join(manifests, "hf.co")
	writeManifest(t, manifests, []string{"hf.co", "org", "model", "Q4"},
		`{"config":{"digest":"sha256-c2","size":5},"layers":[{"digest":"sha256-a","size":10}]}`)

	w, _ := blockingWalker(t, gatedBlobs, gatedRegistry)
	type result struct {
		hf, ollama []engine.Item
	}
	done := make(chan result, 1)
	go func() {
		hf, _ := scanHFHub(context.Background(), w, hub)
		ol, _ := scanOllamaModels(context.Background(), w, manifests)
		done <- result{hf, ol}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(60 * testStall):
		t.Fatal("hf/ollama walkers did not return")
	}

	partialByLabel := map[string]bool{}
	for _, it := range r.hf {
		partialByLabel[it.Label] = it.Partial
	}
	if want := map[string]bool{"org/open": false, "org/gated": true}; len(r.hf) != 2 || !maps.Equal(partialByLabel, want) {
		t.Errorf("hf items = %+v, want the open repo complete and the gated one Partial", r.hf)
	}
	var partial int
	for _, it := range r.ollama {
		if it.Partial {
			partial++
		}
	}
	if len(r.ollama) != 2 || partial != 1 || r.ollama[0].Arg != "llama3:latest" {
		t.Errorf("ollama items = %+v, want llama3 plus one unreadable marker", r.ollama)
	}
}

// The process-level proof: a scan whose blocked folder never answers
// returns, and the process that ran it exits, with the stuck goroutine
// still in place. The test binary re-runs itself as that process.
func TestBlockedScanProcessExits(t *testing.T) {
	if os.Getenv("REGROW_BLOCKED_SCAN_CHILD") == "1" {
		home, _, blocked := fixtureTree(t)
		w, _ := blockingWalkerUntil(make(chan struct{}), blocked) // never released
		s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: w}
		f := s.Scan(context.Background(), []engine.Rule{pathRule("cache", "~/cache")})
		out, _ := json.Marshal(f[0].Items)
		_, _ = os.Stdout.WriteString("ITEMS " + string(out) + "\n")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBlockedScanProcessExits$", "-test.v")
	cmd.Env = append(os.Environ(), "REGROW_BLOCKED_SCAN_CHILD=1")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("child process did not exit within 60s:\n%s", out)
	}
	if err != nil {
		t.Fatalf("child process failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"partial":true`) {
		t.Fatalf("child did not report the blocked item as partial:\n%s", out)
	}
	t.Logf("child exited in %s", time.Since(start).Round(time.Millisecond))
}

// A written-off folder stays blocked only while its call is stuck:
// once the call answers (the owner granted access), the next scan
// reads the folder again.
func TestBlockedFolderIsReadAgainOnceItAnswers(t *testing.T) {
	home, _, blocked := fixtureTree(t)
	release := make(chan struct{})
	w, opens := blockingWalkerUntil(release, blocked)
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: w}
	rule := pathRule("cache", "~/cache")

	if it := scanWithin(t, s, rule)[0].Items[0]; !it.Partial {
		t.Fatalf("first scan = %+v, want Partial", it)
	}
	close(release)

	deadline := time.Now().Add(20 * testStall)
	for {
		it := scanWithin(t, s, rule)[0].Items[0]
		if !it.Partial {
			if it.Bytes < 560_000 {
				t.Fatalf("bytes = %d, want the whole tree once the folder answered", it.Bytes)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("folder stayed blocked after its call answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := opens.Load(); n != 2 {
		t.Errorf("blocked folder opened %d times, want 2: the stuck call and one retry after it answered", n)
	}
}
