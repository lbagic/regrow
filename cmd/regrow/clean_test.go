package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

type memJournal struct{ entries []oplog.Entry }

func (j *memJournal) Append(e oplog.Entry) error {
	j.entries = append(j.entries, e)
	return nil
}

// noMover fails the test if a plan of native commands reaches the Trash.
type noMover struct{ t *testing.T }

func (m noMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	m.t.Errorf("Trash move of %s: the plan holds no trash action", path)
	return trash.Receipt{}, nil
}

// fixtureGit runs git for a throwaway repository, cut off from the
// machine's config and from the variables a git hook or `rebase --exec`
// sets, which would point it at the enclosing repository.
func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestWorktreeRecheckStopsTheRemoval drives the hooks of the run
// executor `regrow clean` builds, with the command and the journal
// injected: the removal runs for a worktree unchanged since planning,
// and never runs once an ignored file appeared in it.
func TestWorktreeRecheckStopsTheRemoval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// The hook's own git calls read the process environment.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "home", "workspace", "app")
	wt := filepath.Join(repo, ".claude", "worktrees", "done")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", ".gitignore")
	fixtureGit(t, repo, "commit", "-q", "-m", "init")
	fixtureGit(t, repo, "worktree", "add", "-q", "-b", "done", wt)

	host := engine.Host{OS: "darwin", Home: filepath.Join(root, "home")}
	rule := engine.Rule{
		ID: "git-worktrees", Risk: engine.RiskCaution,
		NativeCommand: engine.Argv{"git", "-C", "{path}", "worktree", "remove", "{path}"},
		PreAction:     engine.PreActionWorktreeRecheck,
	}
	findings := []engine.Finding{{Rule: rule, Items: []engine.Item{{Path: wt, Bytes: 10}}}}
	plan := engine.BuildPlan(host, findings, map[string]bool{rule.ID: true})
	if len(plan.Actions) != 1 || plan.Actions[0].PreAction != engine.PreActionWorktreeRecheck {
		t.Fatalf("plan = %+v, want one action carrying the recheck", plan)
	}

	var ran [][]string
	journal := &memJournal{}
	exe, release, err := newRunExecutor(host, "fixture-run", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe.Trash = noMover{t}
	exe.Log = journal
	exe.RunNative = func(_ context.Context, argv []string) error {
		ran = append(ran, argv)
		return nil
	}

	res, err := exe.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"git", "-C", wt, "worktree", "remove", wt}}
	if res.Done != 1 || res.Failed != 0 || !reflect.DeepEqual(ran, want) {
		t.Fatalf("unchanged worktree: result %+v, ran %v, want the removal to run once", res, ran)
	}

	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal.entries = nil
	res, err = exe.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 0 || res.Failed != 1 || len(ran) != 1 {
		t.Fatalf("after an ignored .env appeared: result %+v, ran %v, want one failure and no second removal", res, ran)
	}
	if len(journal.entries) != 2 || journal.entries[1].Event != oplog.EventFail {
		t.Fatalf("journal = %+v, want a start line and a fail line", journal.entries)
	}
	if msg := journal.entries[1].Error; !strings.Contains(msg, ".env") || !strings.Contains(msg, wt) {
		t.Fatalf("journaled error = %q, want it to name the file and the worktree", msg)
	}
}
