package scanner

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Synthetic session ids: one letter per role in the fixture.
const (
	sidEnded      = "aaaaaaaa-0000-4000-8000-000000000001"
	sidRunning    = "bbbbbbbb-0000-4000-8000-000000000002"
	sidChatting   = "cccccccc-0000-4000-8000-000000000003"
	sidOldChat    = "dddddddd-0000-4000-8000-000000000004"
	sidWriting    = "eeeeeeee-0000-4000-8000-000000000005"
	sidWorktree   = "ffffffff-0000-4000-8000-000000000006"
	sidInnerLinks = "abababab-0000-4000-8000-000000000007"
	sidOrphanWT   = "cdcdcdcd-0000-4000-8000-000000000008"
	sidLocked     = "efefefef-0000-4000-8000-000000000009"
	sidRepo       = "fafafafa-0000-4000-8000-00000000000a"
)

// ageTree sets every mtime under root to at.
func ageTree(t *testing.T, root string, at time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// agentFixture lays out a scratch root holding one session per role,
// plus entries that are not sessions, all last changed three days ago
// unless the role says otherwise. Everything, git links included, is
// written through a symlinked folder; real is the root's real path.
func agentFixture(t *testing.T, now time.Time) (root, real string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "via")
	if err := os.Mkdir(filepath.Join(filepath.Dir(tmp), "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", tmp); err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(tmp, "claude-1000")
	repo := filepath.Join(tmp, "repo", ".git", "worktrees", "wt")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "-Users-dev-proj")
	other := filepath.Join(root, "-Users-dev-other")
	for _, sid := range []string{sidEnded, sidRunning, sidChatting, sidOldChat, sidWorktree, sidInnerLinks, sidOrphanWT, sidLocked, sidRepo} {
		writeFile(t, filepath.Join(proj, sid, "scratchpad", "notes.txt"), 40_000)
	}
	writeFile(t, filepath.Join(other, sidWriting, "tasks", "job.output"), 1000)
	writeText(t, filepath.Join(proj, sidWorktree, "scratchpad", "wt", ".git"), "gitdir: "+repo+"\n")
	if err := os.MkdirAll(filepath.Join(proj, sidInnerLinks, "clone", ".git", "modules", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", "sub", ".git"), "gitdir: ../.git/modules/sub\n")
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", "inner", ".git"), "gitdir: ../.git/worktrees/inner\n")
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", ".git", "worktrees", "inner", "gitdir"), filepath.Join(proj, sidInnerLinks, "clone", "inner", ".git")+"\n")
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", ".git", "worktrees", "pruned", "gitdir"), filepath.Join(tmp, "gone", "pruned", ".git")+"\n")
	elsewhere := filepath.Join(tmp, "elsewhere", "wt", ".git")
	writeText(t, elsewhere, "gitdir: "+filepath.Join(proj, sidRepo, "clone", ".git", "worktrees", "wt")+"\n")
	writeText(t, filepath.Join(proj, sidRepo, "clone", ".git", "worktrees", "wt", "gitdir"), elsewhere+"\n")
	writeText(t, filepath.Join(proj, sidOrphanWT, "wt", ".git"), "gitdir: "+filepath.Join(tmp, "gone", ".git", "worktrees", "wt")+"\n")
	writeFile(t, filepath.Join(proj, "not-a-session", "x"), 1000)
	writeFile(t, filepath.Join(proj, "cache-state.json"), 10)
	writeFile(t, filepath.Join(root, "stray.txt"), 10)

	ageTree(t, root, now.Add(-72*time.Hour))
	locked := filepath.Join(proj, sidLocked, "scratchpad")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	recent := now.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(other, sidWriting, "tasks", "job.output"), recent, recent); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real == root {
		t.Fatalf("fixture root must be reached through a symlink: %q, %v", real, err)
	}
	return root, real
}

func TestAgentSessionsSplit(t *testing.T) {
	now := time.Now()
	root, real := agentFixture(t, now)
	runningCalls := 0
	transcripts := map[string]time.Time{
		"-Users-dev-proj/" + sidChatting: now.Add(-2 * time.Hour),
		"-Users-dev-proj/" + sidOldChat:  now.Add(-48 * time.Hour),
		// Same id under another project: must not be read for sidEnded.
		"-Users-dev-other/" + sidEnded: now.Add(-time.Hour),
	}
	a := &agentSessions{
		Root: root,
		Running: func(context.Context) (map[string]bool, error) {
			runningCalls++
			return map[string]bool{sidRunning: true}, nil
		},
		Activity: func(_ context.Context, project, id string) time.Time {
			return transcripts[project+"/"+id]
		},
		Now: func() time.Time { return now },
		w:   testWalker(),
	}

	ended, err := a.Ended(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kept, err := a.Kept(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runningCalls != 1 {
		t.Errorf("process lister called %d times, want once per scan", runningCalls)
	}

	byArg := func(items []engine.Item) map[string]engine.Item {
		m := map[string]engine.Item{}
		for _, it := range items {
			m[it.Arg] = it
		}
		return m
	}
	gotEnded := byArg(ended)
	if ids := slices.Sorted(maps.Keys(gotEnded)); !slices.Equal(ids, []string{sidEnded, sidInnerLinks, sidOrphanWT, sidOldChat}) {
		t.Errorf("ended sessions = %v", ids)
	}
	for _, it := range slices.Concat(ended, kept) {
		if want := filepath.Join(real, filepath.Base(filepath.Dir(it.Path)), it.Arg); it.Path != want {
			t.Errorf("item %q: path %q, want its session dir by real path %q", it.Arg, it.Path, want)
		}
	}
	for _, it := range ended {
		if it.Bytes < 40_000 {
			t.Errorf("ended item %q measured %d bytes, want its scratch (≥ 40000)", it.Arg, it.Bytes)
		}
	}
	if got, want := gotEnded[sidOldChat].LastUsed, now.Add(-48*time.Hour); !got.Equal(want) {
		t.Errorf("last used of a session whose transcript is newer than its scratch = %v, want the transcript's %v", got, want)
	}

	wantKept := map[string]string{
		sidRunning:  "running",
		sidChatting: "transcript changed within 24h",
		sidWriting:  "scratch changed within 24h",
		sidWorktree: "holds git worktree scratchpad/wt",
		sidLocked:   "not fully readable",
		sidRepo:     "holds repository clone with a worktree elsewhere",
	}
	gotKept := byArg(kept)
	if len(gotKept) != len(wantKept) {
		t.Errorf("kept %d sessions, want %d: %+v", len(gotKept), len(wantKept), kept)
	}
	for sid, reason := range wantKept {
		it, ok := gotKept[sid]
		if !ok {
			t.Errorf("session %s not kept (want %q)", sid, reason)
			continue
		}
		if !strings.Contains(it.Label, reason) {
			t.Errorf("kept session %s label %q, want reason %q", sid, it.Label, reason)
		}
	}
}

func TestAgentSessionsUnknownLivenessOffersNothing(t *testing.T) {
	now := time.Now()
	root, _ := agentFixture(t, now)
	a := &agentSessions{
		Root:     root,
		Running:  func(context.Context) (map[string]bool, error) { return nil, errors.New("ps failed") },
		Activity: func(context.Context, string, string) time.Time { return time.Time{} },
		Now:      func() time.Time { return now },
		w:        testWalker(),
	}
	ended, err := a.Ended(context.Background())
	if err == nil || len(ended) != 0 {
		t.Fatalf("Ended = %d items, err %v; want an error and nothing offered", len(ended), err)
	}
	if _, err := a.Kept(context.Background()); err == nil {
		t.Error("Kept: want the same error")
	}
}

func TestAgentSessionsNoRoot(t *testing.T) {
	a := &agentSessions{
		Root: filepath.Join(t.TempDir(), "claude-1000"),
		Running: func(context.Context) (map[string]bool, error) {
			t.Fatal("no sessions: liveness must not be asked")
			return nil, nil
		},
		w: testWalker(),
	}
	ended, err := a.Ended(context.Background())
	kept, _ := a.Kept(context.Background())
	if err != nil || len(ended)+len(kept) != 0 {
		t.Errorf("missing root: ended %v, kept %v, err %v; want nothing", ended, kept, err)
	}
}

func TestClaudeRunning(t *testing.T) {
	const live, crashed = "11111111-aaaa-4aaa-8aaa-111111111111", "22222222-aaaa-4aaa-8aaa-222222222222"
	config := t.TempDir()
	sessions := filepath.Join(config, "sessions")
	writeText(t, filepath.Join(sessions, "100.json"), `{"pid":100,"sessionId":"`+live+`","kind":"interactive"}`)
	writeText(t, filepath.Join(sessions, "200.json"), `{"pid":200,"sessionId":"`+crashed+`"}`)
	writeText(t, filepath.Join(sessions, "300.json"), `{not json`)
	writeText(t, filepath.Join(sessions, "100.abc.key"), `{"pid":100,"sessionId":"not-a-registry-file"}`)

	cases := []struct {
		name    string
		config  string
		procs   map[int]string
		want    []string
		wantErr bool
	}{
		{"live pid counts, dead pid does not", config, map[int]string{1: "launchd", 100: "claude"}, []string{live}, false},
		{"no claude running, nothing registered", t.TempDir(), map[int]string{1: "launchd"}, nil, false},
		{"claude running but the registry names none", t.TempDir(), map[int]string{1: "launchd", 400: "/opt/bin/claude"}, nil, true},
		{"registry names only dead pids", config, map[int]string{1: "launchd", 400: "claude"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			procs := func(context.Context) (map[int]string, error) { return tc.procs, nil }
			got, err := claudeRunning(context.Background(), testWalker(), tc.config, procs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if ids := slices.Sorted(maps.Keys(got)); !slices.Equal(ids, tc.want) {
				t.Errorf("running sessions = %v, want %v", ids, tc.want)
			}
		})
	}
}

func TestClaudeActivity(t *testing.T) {
	const sid = "33333333-aaaa-4aaa-8aaa-333333333333"
	config := t.TempDir()
	proj := filepath.Join(config, "projects", "-Users-dev-proj")
	transcript := filepath.Join(proj, sid+".jsonl")
	subagent := filepath.Join(proj, sid, "subagents", "agent-1.jsonl")
	writeText(t, transcript, "{}\n")
	writeText(t, subagent, "{}\n")
	base := time.Now().Add(-100 * time.Hour).Truncate(time.Second)
	ageTree(t, proj, base)
	if err := os.Chtimes(subagent, base.Add(5*time.Hour), base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if got := claudeActivity(context.Background(), testWalker(), config, "-Users-dev-proj", sid); !got.Equal(base.Add(5 * time.Hour)) {
		t.Errorf("activity = %v, want the subagent transcript's %v", got, base.Add(5*time.Hour))
	}
	if err := os.Chtimes(transcript, base.Add(9*time.Hour), base.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := claudeActivity(context.Background(), testWalker(), config, "-Users-dev-proj", sid); !got.Equal(base.Add(9 * time.Hour)) {
		t.Errorf("activity = %v, want the main transcript's %v", got, base.Add(9*time.Hour))
	}
	if got := claudeActivity(context.Background(), testWalker(), config, "-Users-dev-other", sid); !got.IsZero() {
		t.Errorf("activity under another project = %v, want none", got)
	}
}

func TestParsePS(t *testing.T) {
	out := "    1 /sbin/launchd\n  412 claude\n 9001 /Applications/Some App.app/Contents/MacOS/Some App\n\nbogus\n"
	got := parsePS(out)
	want := map[int]string{1: "/sbin/launchd", 412: "claude", 9001: "/Applications/Some App.app/Contents/MacOS/Some App"}
	if !maps.Equal(got, want) {
		t.Errorf("parsePS = %v, want %v", got, want)
	}
}
