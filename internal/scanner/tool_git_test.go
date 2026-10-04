package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// gitTestEnv isolates git from the machine's config and fixes the
// author and dates, so the throwaway repositories are reproducible.
func gitTestEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "Fixture")
		t.Setenv("GIT_"+who+"_EMAIL", "fixture@example.com")
		t.Setenv("GIT_"+who+"_DATE", "2026-01-01T00:00:00Z")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", "change "+name)
}

// worktreeFixture is a main repository with one linked worktree per
// case, each failing or passing exactly one test of the rule.
type worktreeFixture struct {
	home, app, scratch, solo string
	wt                       map[string]string // case → worktree path
	now                      time.Time         // a week and a day after the fixture was built
}

func newWorktreeFixture(t *testing.T) worktreeFixture {
	t.Helper()
	gitTestEnv(t)
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real // git reports real paths
	}
	f := worktreeFixture{
		home:    filepath.Join(root, "home"),
		scratch: filepath.Join(root, "scratch"),
		wt:      map[string]string{},
		now:     time.Now().Add(worktreeIdle + 24*time.Hour),
	}
	f.app = filepath.Join(f.home, "workspace", "app")
	if err := os.MkdirAll(f.app, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.app, "init", "-q", "-b", "main")
	runGit(t, f.app, "remote", "add", "origin", filepath.Join(root, "origin.git"))
	commitFile(t, f.app, ".gitignore", "node_modules/\n.env\n")
	commitFile(t, f.app, "README", "app\n")

	add := func(name string, args ...string) string {
		p := filepath.Join(f.app, ".claude", "worktrees", name)
		runGit(t, f.app, append([]string{"worktree", "add", "-q", p}, args...)...)
		f.wt[name] = p
		return p
	}
	branch := func(name string) string { return add(name, "-b", name) }
	detached := func(name string) string { return add(name, "--detach", "main") }

	// Qualifies: merged by ancestry, holding ignored node_modules.
	p := branch("merged")
	commitFile(t, p, "feature", "done\n")
	touch(t, filepath.Join(p, "node_modules", "dep", "index.js"))
	runGit(t, f.app, "merge", "-q", "--ff-only", "merged")

	// Qualifies: squash-merged elsewhere, its upstream branch deleted.
	p = branch("squashed")
	commitFile(t, p, "fix", "done\n")
	runGit(t, f.app, "config", "branch.squashed.remote", "origin")
	runGit(t, f.app, "config", "branch.squashed.merge", "refs/heads/squashed")

	// Qualifies: detached at a commit main contains.
	detached("detached-merged")

	// Qualifies: another tool's plain `git status` stamped the admin
	// dir and its index, which is not work in the worktree.
	branch("status-refreshed")

	// Each of these fails exactly one test.
	p = branch("unmerged")
	commitFile(t, p, "wip", "half\n")

	p = detached("detached-unmerged")
	commitFile(t, p, "experiment", "x\n")

	p = branch("untracked")
	touch(t, filepath.Join(p, "notes.txt"))

	p = branch("modified")
	if err := os.WriteFile(filepath.Join(p, "README"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p = branch("ignored-env")
	touch(t, filepath.Join(p, ".env"))

	p = branch("active")
	touch(t, filepath.Join(p, "node_modules", "dep", "index.js"))
	recent := f.now.Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(p, "node_modules", "dep", "index.js"), recent, recent); err != nil {
		t.Fatal(err)
	}

	// A commit records itself in the admin dir; the files can be older.
	branch("committed-recently")
	stamp := func(wt string, names ...string) {
		admin := runGit(t, wt, "rev-parse", "--absolute-git-dir")
		for _, name := range names {
			if err := os.Chtimes(filepath.Join(admin, name), recent, recent); err != nil {
				t.Fatal(err)
			}
		}
	}
	stamp(f.wt["committed-recently"], filepath.Join("logs", "HEAD"))
	stamp(f.wt["status-refreshed"], "", "index")

	branch("locked")
	runGit(t, f.app, "worktree", "lock", f.wt["locked"])

	gone := branch("prunable")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	// A worktree of app in an agent's scratch dir, which is also a
	// root, and a scratch clone with a worktree of its own.
	p = filepath.Join(f.scratch, "session", "wt")
	runGit(t, f.app, "worktree", "add", "-q", "-b", "scratch", p, "main")
	f.wt["scratch"] = p
	f.solo = filepath.Join(f.scratch, "session", "solo")
	if err := os.MkdirAll(f.solo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.solo, "init", "-q", "-b", "main")
	commitFile(t, f.solo, "README", "solo\n")
	p = filepath.Join(f.scratch, "session", "solo-wt")
	runGit(t, f.solo, "worktree", "add", "-q", "--detach", p, "main")
	f.wt["solo"] = p
	return f
}

func (f worktreeFixture) scan(t *testing.T) []engine.Item {
	t.Helper()
	host := engine.Host{OS: "darwin", Home: f.home}
	items, err := scanWorktrees(context.Background(), testWalker(), host, []string{"~/workspace", f.scratch}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestScanWorktreesOffersOnlyFinishedOnes(t *testing.T) {
	f := newWorktreeFixture(t)
	items := f.scan(t)

	got := map[string]string{}
	for _, it := range items {
		got[it.Path] = it.Label
		if it.Bytes <= 0 || it.LastUsed.IsZero() || it.Partial {
			t.Errorf("%s: want a complete measurement, got %+v", it.Label, it)
		}
	}
	mainHead := runGit(t, f.app, "rev-parse", "--short=7", "main")
	soloHead := runGit(t, f.solo, "rev-parse", "--short=7", "main")
	want := map[string]string{
		f.wt["merged"]:           "merged (merged)",
		f.wt["squashed"]:         "squashed (upstream gone)",
		f.wt["detached-merged"]:  "detached " + mainHead + " (merged)",
		f.wt["status-refreshed"]: "status-refreshed (merged)",
		f.wt["scratch"]:          "scratch (merged)",
		f.wt["solo"]:             "detached " + soloHead + " (merged)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v\nwant %v", got, want)
	}
}

func TestScanWorktreesIsOneItemPerWorktree(t *testing.T) {
	f := newWorktreeFixture(t)
	finding := engine.Finding{Rule: worktreeRule(t), Items: f.scan(t)}
	finding.FillItemKeys(f.home)

	plan := engine.BuildPlan(engine.Host{OS: "darwin", Home: f.home}, []engine.Finding{finding}, map[string]bool{"git-worktrees": true})
	if len(plan.Actions) != len(finding.Items) || len(plan.Skipped) != 0 {
		t.Fatalf("want one action per worktree, got %d actions for %d items (skips %+v)", len(plan.Actions), len(finding.Items), plan.Skipped)
	}
	for i, a := range plan.Actions {
		path := finding.Items[i].Path
		if want := []string{"git", "-C", path, "worktree", "remove", path}; !reflect.DeepEqual(a.Command, want) {
			t.Errorf("action %d = %v, want %v", i, a.Command, want)
		}
		if a.ItemKey != finding.Items[i].Key {
			t.Errorf("action %d targets key %q, want the item's own %q", i, a.ItemKey, finding.Items[i].Key)
		}
	}
}

// TestWorktreeRemoveCommand runs the rule's command on the throwaway
// repository: the worktree goes with its ignored build output, the
// branch stays, and git refuses a worktree with untracked files.
func TestWorktreeRemoveCommand(t *testing.T) {
	f := newWorktreeFixture(t)
	rule := worktreeRule(t)
	run := func(path string) error {
		argv, err := rule.NativeCommand.ExpandItem(engine.Item{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		return exec.Command(argv[0], argv[1:]...).Run()
	}

	if err := run(f.wt["merged"]); err != nil {
		t.Fatalf("remove merged worktree: %v", err)
	}
	if _, err := os.Lstat(f.wt["merged"]); !os.IsNotExist(err) {
		t.Fatalf("worktree folder still there (err %v)", err)
	}
	if runGit(t, f.app, "branch", "--list", "merged") == "" {
		t.Fatal("the branch must survive the worktree")
	}
	if strings.Contains(runGit(t, f.app, "worktree", "list"), f.wt["merged"]) {
		t.Fatal("git still lists the removed worktree")
	}

	if err := run(f.wt["untracked"]); err == nil {
		t.Fatal("git removed a worktree with untracked files")
	}
	if _, err := os.Lstat(filepath.Join(f.wt["untracked"], "notes.txt")); err != nil {
		t.Fatalf("the untracked file must survive: %v", err)
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /r\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /r/w1\x00HEAD bbb\x00detached\x00locked\x00\x00" +
		"worktree /r/w2\x00HEAD ccc\x00branch refs/heads/x\x00prunable gitdir file points to non-existent location\x00\x00"
	got := parseWorktreeList([]byte(out))
	want := []worktree{
		{path: "/r", head: "aaa", branch: "refs/heads/main"},
		{path: "/r/w1", head: "bbb", locked: true},
		{path: "/r/w2", head: "ccc", branch: "refs/heads/x", prunable: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %+v\nwant %+v", got, want)
	}
}

func worktreeRule(t *testing.T) engine.Rule {
	t.Helper()
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		if r.ID == "git-worktrees" {
			return r
		}
	}
	t.Fatal("git-worktrees rule missing from the catalog")
	return engine.Rule{}
}
