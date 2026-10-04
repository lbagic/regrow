package scanner

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Synthetic session ids, one per role in the fixture.
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
	sidHost       = "acacacac-0000-4000-8000-00000000000b"
	sidTool       = "adadadad-0000-4000-8000-00000000000c"
	sidTornLog    = "aeaeaeae-0000-4000-8000-00000000000d"
	sidVanished   = "afafafaf-0000-4000-8000-00000000000e"

	projDir  = "-Users-dev-proj"
	otherDir = "-Users-dev-other"
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
	proj := filepath.Join(root, projDir)
	other := filepath.Join(root, otherDir)
	for _, sid := range []string{
		sidEnded, sidRunning, sidChatting, sidOldChat, sidWorktree, sidInnerLinks, sidOrphanWT,
		sidLocked, sidRepo, sidHost, sidTool, sidTornLog, sidVanished,
	} {
		writeFile(t, filepath.Join(proj, sid, "scratchpad", "notes.txt"), 40_000)
	}
	writeFile(t, filepath.Join(other, sidWriting, "tasks", "job.output"), 1000)
	// One session id with scratch under two projects.
	writeFile(t, filepath.Join(other, sidEnded, "scratchpad", "notes.txt"), 1000)
	writeFile(t, filepath.Join(proj, sidHost, "scratchpad", "clone", "main.go"), 1000)
	writeFile(t, filepath.Join(proj, sidTool, "node_modules", ".bin", "tool"), 1000)
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

// sessionKey names an item by project and session id.
func sessionKey(it engine.Item) string {
	return filepath.Base(filepath.Dir(it.Path)) + "/" + filepath.Base(it.Path)
}

func TestAgentSessionsSplit(t *testing.T) {
	now := time.Now()
	root, real := agentFixture(t, now)
	liveCalls := 0
	type activity struct {
		at     time.Time
		unread bool
	}
	transcripts := map[string]activity{
		projDir + "/" + sidChatting: {at: now.Add(-2 * time.Hour)},
		projDir + "/" + sidOldChat:  {at: now.Add(-48 * time.Hour)},
		projDir + "/" + sidTornLog:  {unread: true},
		// The same id under another project: read for that scratch only.
		otherDir + "/" + sidEnded: {at: now.Add(-time.Hour)},
	}
	a := &agentSessions{
		Root: root,
		Live: func(context.Context) (liveness, error) {
			liveCalls++
			// Gone between listing and measuring.
			if err := os.RemoveAll(filepath.Join(real, projDir, sidVanished)); err != nil {
				t.Fatal(err)
			}
			return liveness{
				sessions: map[string]bool{sidRunning: true},
				paths: []string{
					filepath.Join(real, projDir, sidHost, "scratchpad", "clone"),
					filepath.Join(real, projDir, sidTool, "node_modules", ".bin", "tool"),
					filepath.Join(real, projDir, sidEnded+"-sibling", "bin", "tool"),
				},
			}, nil
		},
		Activity: func(_ context.Context, project, id string) (time.Time, bool) {
			tr := transcripts[project+"/"+id]
			return tr.at, !tr.unread
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
	if liveCalls != 1 {
		t.Errorf("process lister called %d times, want once per scan", liveCalls)
	}

	for _, it := range slices.Concat(ended, kept) {
		if !strings.HasPrefix(it.Path, real+"/") || !sessionIDRe.MatchString(filepath.Base(it.Path)) {
			t.Errorf("item path %q: want a session dir under the real root %q", it.Path, real)
		}
		if key := it.DeriveKey(""); key != it.Path {
			t.Errorf("item %q is keyed %q, want its path: one session id can own scratch under two projects", it.Path, key)
		}
	}

	var gotEnded []string
	for _, it := range ended {
		gotEnded = append(gotEnded, sessionKey(it))
		if it.Bytes < 40_000 {
			t.Errorf("ended item %q measured %d bytes, want its scratch (≥ 40000)", sessionKey(it), it.Bytes)
		}
		if sessionKey(it) == projDir+"/"+sidOldChat && !it.LastUsed.Equal(now.Add(-48*time.Hour)) {
			t.Errorf("last used of a session whose transcript is newer than its scratch = %v, want the transcript's", it.LastUsed)
		}
	}
	slices.Sort(gotEnded)
	wantEnded := []string{projDir + "/" + sidEnded, projDir + "/" + sidInnerLinks, projDir + "/" + sidOrphanWT, projDir + "/" + sidOldChat}
	if !slices.Equal(gotEnded, wantEnded) {
		t.Errorf("ended sessions = %v, want %v", gotEnded, wantEnded)
	}

	wantKept := map[string]string{
		projDir + "/" + sidRunning:  "running",
		projDir + "/" + sidHost:     "a running process works inside it",
		projDir + "/" + sidTool:     "a running process works inside it",
		projDir + "/" + sidChatting: "transcript changed within 24h",
		otherDir + "/" + sidEnded:   "transcript changed within 24h",
		otherDir + "/" + sidWriting: "scratch changed within 24h",
		projDir + "/" + sidTornLog:  "transcript not fully readable",
		projDir + "/" + sidLocked:   "not fully readable, so git worktrees",
		projDir + "/" + sidWorktree: "holds git worktree scratchpad/wt; remove the worktree first",
		projDir + "/" + sidRepo:     "holds repository clone with a worktree elsewhere",
	}
	gotKept := map[string]string{}
	for _, it := range kept {
		gotKept[sessionKey(it)] = it.Label
	}
	if len(gotKept) != len(wantKept) {
		t.Errorf("kept %d sessions, want %d: %v", len(gotKept), len(wantKept), slices.Sorted(maps.Keys(gotKept)))
	}
	for key, reason := range wantKept {
		if label, ok := gotKept[key]; !ok {
			t.Errorf("session %s not kept (want %q)", key, reason)
		} else if !strings.Contains(label, " — "+reason) {
			t.Errorf("kept session %s label %q, want reason %q", key, label, reason)
		}
	}
}

func TestAgentSessionsUnknownLivenessOffersNothing(t *testing.T) {
	now := time.Now()
	root, _ := agentFixture(t, now)
	a := &agentSessions{
		Root:     root,
		Live:     func(context.Context) (liveness, error) { return liveness{}, errors.New("ps failed") },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
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
		Live: func(context.Context) (liveness, error) {
			t.Fatal("no sessions: liveness must not be asked")
			return liveness{}, nil
		},
		w: testWalker(),
	}
	ended, err := a.Ended(context.Background())
	kept, _ := a.Kept(context.Background())
	if err != nil || len(ended)+len(kept) != 0 {
		t.Errorf("missing root: ended %v, kept %v, err %v; want nothing", ended, kept, err)
	}
}

// A guest session runs with its working directory inside a host
// session's scratch, and a tool started from a third session's scratch
// is still running. The host and the tool's session have no process of
// their own, yet both must be kept, through the registry and ps alone.
func TestAgentSessionsHostGuest(t *testing.T) {
	const host, guest, tool, ended = sidHost, sidRunning, sidTool, sidEnded
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	for _, sid := range []string{host, guest, tool, ended} {
		writeFile(t, filepath.Join(root, projDir, sid, "scratchpad", "notes.txt"), 1000)
	}
	clone := filepath.Join(root, projDir, host, "scratchpad", "clone")
	exe := filepath.Join(root, projDir, tool, "node_modules", ".bin", "tool")
	writeFile(t, filepath.Join(clone, "main.go"), 100)
	writeFile(t, exe, 100)
	ageTree(t, root, now.Add(-72*time.Hour))

	config := t.TempDir()
	writeText(t, filepath.Join(config, "sessions", "700.json"), `{"pid":700,"sessionId":"`+guest+`","cwd":"`+clone+`"}`)
	procs := func(context.Context) (map[int]string, error) {
		return map[int]string{1: "/sbin/launchd", 700: "claude", 701: exe}, nil
	}
	w := testWalker()
	a := &agentSessions{
		Root: root,
		Live: func(ctx context.Context) (liveness, error) {
			return claudeLiveness(ctx, w, config, procs, func(context.Context, int) string { return "" })
		},
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        w,
	}
	endedItems, err := a.Ended(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keptItems, _ := a.Kept(context.Background())

	if len(endedItems) != 1 || filepath.Base(endedItems[0].Path) != ended {
		t.Errorf("ended = %+v, want only the session nothing touches", endedItems)
	}
	want := map[string]string{host: "a running process works inside it", tool: "a running process works inside it", guest: "running"}
	for _, it := range keptItems {
		sid := filepath.Base(it.Path)
		if reason, ok := want[sid]; !ok || !strings.HasSuffix(it.Label, " — "+reason) {
			t.Errorf("kept %s with label %q, want reason %q", sid, it.Label, want[sid])
		}
		delete(want, sid)
	}
	if len(want) != 0 {
		t.Errorf("not kept: %v", want)
	}
}

func TestRecheckAgentScratch(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "claude-1000", projDir, sidEnded)
	writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
	writeFile(t, filepath.Join(root, "claude-1000", projDir, "not-a-session", "x"), 10)
	ageTree(t, root, now.Add(-72*time.Hour))

	quiet := func(context.Context) (liveness, error) { return liveness{sessions: map[string]bool{}}, nil }
	cases := []struct {
		name    string
		path    string
		live    func(context.Context) (liveness, error)
		changed time.Time
		wantErr string
	}{
		{"still ended", session, quiet, time.Time{}, ""},
		{"resumed since the scan", session, func(context.Context) (liveness, error) {
			return liveness{sessions: map[string]bool{sidEnded: true}}, nil
		}, time.Time{}, "running"},
		{"a process moved in", session, func(context.Context) (liveness, error) {
			return liveness{paths: []string{filepath.Join(session, "scratchpad")}}, nil
		}, time.Time{}, "works inside"},
		{"transcript written since", session, quiet, now.Add(-time.Minute), "transcript changed"},
		{"liveness unknown", session, func(context.Context) (liveness, error) {
			return liveness{}, errors.New("ps failed")
		}, time.Time{}, "cannot tell"},
		{"already gone", filepath.Join(filepath.Dir(session), sidVanished), quiet, time.Time{}, "is gone"},
		{"not a session", filepath.Join(filepath.Dir(session), "not-a-session"), quiet, time.Time{}, "not an agent session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &agentSessions{
				Live:     tc.live,
				Activity: func(context.Context, string, string) (time.Time, bool) { return tc.changed, true },
				Now:      func() time.Time { return now },
				w:        testWalker(),
			}
			err := a.recheck(context.Background(), tc.path)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("recheck refused an ended session: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("recheck = %v, want a refusal naming %q", err, tc.wantErr)
			}
		})
	}
}

func TestClaudeLiveness(t *testing.T) {
	const live, crashed, elsewhere = "11111111-aaaa-4aaa-8aaa-111111111111", "22222222-aaaa-4aaa-8aaa-222222222222", "33333333-aaaa-4aaa-8aaa-333333333333"
	registry := func(t *testing.T, files map[string]string) string {
		t.Helper()
		config := t.TempDir()
		for name, text := range files {
			writeText(t, filepath.Join(config, "sessions", name), text)
		}
		return config
	}
	files := map[string]string{
		"100.json":    `{"pid":100,"sessionId":"` + live + `","cwd":"/work/proj","kind":"interactive"}`,
		"200.json":    `{"pid":200,"sessionId":"` + crashed + `","cwd":"/work/crashed"}`,
		"300.json":    `{"pid":300,"sess`,
		"100.abc.key": `{"sessionId":"not-a-registry-file"}`,
	}
	other := registry(t, map[string]string{"500.json": `{"pid":500,"sessionId":"` + elsewhere + `","cwd":"/work/other"}`})
	locked := registry(t, files)
	if err := os.Chmod(filepath.Join(locked, "sessions"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "sessions"), 0o755) })

	cases := []struct {
		name      string
		config    string
		procs     map[int]string
		configOf  map[int]string
		want      []string
		wantPaths []string
		wantErr   string
	}{
		{
			name:      "running pid counts; a dead pid's file is skipped, torn or not",
			config:    registry(t, files),
			procs:     map[int]string{1: "/sbin/launchd", 100: "claude", 77: "/opt/tools/bin/esbuild", 78: "zsh"},
			want:      []string{live},
			wantPaths: []string{"/opt/tools/bin/esbuild", "/sbin/launchd", "/work/proj"},
		},
		{
			name:   "no claude running, nothing registered",
			config: t.TempDir(),
			procs:  map[int]string{78: "zsh"},
		},
		{
			name:    "a running pid's registry file is torn",
			config:  registry(t, files),
			procs:   map[int]string{100: "claude", 300: "claude"},
			wantErr: "300.json of running process 300",
		},
		{
			name:    "the registry folder cannot be listed",
			config:  locked,
			procs:   map[int]string{100: "claude"},
			wantErr: "is unreadable",
		},
		{
			name:    "one claude registered, another in no registry",
			config:  registry(t, files),
			procs:   map[int]string{100: "claude", 400: "/opt/bin/claude"},
			wantErr: "claude process 400 is in no session registry",
		},
		{
			name:      "a claude running under another config folder",
			config:    registry(t, files),
			procs:     map[int]string{100: "claude", 500: "claude"},
			configOf:  map[int]string{500: other},
			want:      []string{live, elsewhere},
			wantPaths: []string{"/work/other", "/work/proj"},
		},
		{
			name:     "its own config folder does not name it either",
			config:   registry(t, files),
			procs:    map[int]string{100: "claude", 600: "claude"},
			configOf: map[int]string{600: other},
			wantErr:  "claude process 600 is in no session registry",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			procs := func(context.Context) (map[int]string, error) { return tc.procs, nil }
			configOf := func(_ context.Context, pid int) string { return tc.configOf[pid] }
			got, err := claudeLiveness(context.Background(), testWalker(), tc.config, procs, configOf)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ids := slices.Sorted(maps.Keys(got.sessions)); !slices.Equal(ids, tc.want) {
				t.Errorf("running sessions = %v, want %v", ids, tc.want)
			}
			if !slices.Equal(got.paths, tc.wantPaths) {
				t.Errorf("paths = %v, want %v", got.paths, tc.wantPaths)
			}
		})
	}
}

func TestClaudeActivity(t *testing.T) {
	const sid = "33333333-aaaa-4aaa-8aaa-333333333333"
	config := t.TempDir()
	proj := filepath.Join(config, "projects", projDir)
	transcript := filepath.Join(proj, sid+".jsonl")
	subagent := filepath.Join(proj, sid, "subagents", "agent-1.jsonl")
	writeText(t, transcript, "{}\n")
	writeText(t, subagent, "{}\n")
	base := time.Now().Add(-100 * time.Hour).Truncate(time.Second)
	ageTree(t, proj, base)
	if err := os.Chtimes(subagent, base.Add(5*time.Hour), base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if got, complete := claudeActivity(ctx, testWalker(), config, projDir, sid); !got.Equal(base.Add(5*time.Hour)) || !complete {
		t.Errorf("activity = %v (complete %v), want the subagent transcript's %v", got, complete, base.Add(5*time.Hour))
	}
	if err := os.Chtimes(transcript, base.Add(9*time.Hour), base.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, complete := claudeActivity(ctx, testWalker(), config, projDir, sid); !got.Equal(base.Add(9*time.Hour)) || !complete {
		t.Errorf("activity = %v (complete %v), want the main transcript's %v", got, complete, base.Add(9*time.Hour))
	}
	if got, complete := claudeActivity(ctx, testWalker(), config, otherDir, sid); !got.IsZero() || !complete {
		t.Errorf("activity under another project = %v (complete %v), want none, read in full", got, complete)
	}

	locked := filepath.Join(proj, sid, "subagents")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, complete := claudeActivity(ctx, testWalker(), config, projDir, sid); complete {
		t.Error("a transcript folder that cannot be read must not count as read in full")
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

func TestParseConfigDir(t *testing.T) {
	cases := map[string]string{
		"claude TERM=xterm CLAUDE_CONFIG_DIR=/Users/dev/.claude-work HOME=/Users/dev": "/Users/dev/.claude-work",
		"claude --resume TERM=xterm HOME=/Users/dev":                                  "",
		"claude NOT_CLAUDE_CONFIG_DIR=/Users/dev/x HOME=/Users/dev":                   "",
		"claude CLAUDE_CONFIG_DIR=relative/dir":                                       "",
	}
	for env, want := range cases {
		if got := parseConfigDir(env); got != want {
			t.Errorf("parseConfigDir(%q) = %q, want %q", env, got, want)
		}
	}
}

func TestScanSpelling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("/tmp is a symlink into /private on macOS only")
	}
	cases := map[string]string{
		"/tmp/claude-1000/p/s":         "/private/tmp/claude-1000/p/s",
		"/private/tmp/claude-1000/p/s": "/private/tmp/claude-1000/p/s",
		"/var/folders/x/":              "/private/var/folders/x",
		"/tmpfiles/x":                  "/tmpfiles/x",
		"/Users/dev/proj":              "/Users/dev/proj",
	}
	for in, want := range cases {
		if got := scanSpelling(in); got != want {
			t.Errorf("scanSpelling(%q) = %q, want %q", in, got, want)
		}
	}
}
