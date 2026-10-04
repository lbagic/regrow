package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Linked git worktrees that are finished. A worktree qualifies when all
// three hold: its HEAD is merged into the default branch (by ancestry,
// or its branch's upstream is gone, as after a squash merge and branch
// delete); no commit, checkout or file change touched it for a week;
// and `git status --ignored` shows nothing but whole ignored build
// directories. The rule removes it with `git worktree remove`, which
// keeps the branch, refuses a worktree with modified or untracked
// files, and deletes the ignored build output with the checkout.

const (
	// worktreeIdle is how long a worktree must go untouched.
	worktreeIdle = 7 * 24 * time.Hour
	// worktreeRepoDepth bounds the search for repositories below each
	// root: project trees, and agent scratch dirs, whose clones sit
	// about six levels down.
	worktreeRepoDepth = 6
	// gitCallTimeout bounds one git call; a worktree whose call times
	// out is not offered.
	gitCallTimeout = 30 * time.Second
)

// worktreeBuildDirs are the ignored directories a finished worktree
// may still hold: build output and dependencies its tools regenerate.
// Any other ignored path (an .env file, editor or agent settings) keeps
// the worktree, because removing it would delete that path.
var worktreeBuildDirs = map[string]bool{
	".gradle":       true,
	".mypy_cache":   true,
	".next":         true,
	".nuxt":         true,
	".parcel-cache": true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".svelte-kit":   true,
	".turbo":        true,
	".venv":         true,
	"__pycache__":   true,
	"build":         true,
	"coverage":      true,
	"dist":          true,
	"node_modules":  true,
	"target":        true,
}

// worktreeRoots are where the query looks for repositories: the
// project roots the discover rules use, and agent scratch dirs, where
// agents clone repositories and add worktrees.
func worktreeRoots() []string {
	return []string{"~/workspace", "~/dev", "~/projects", "~/src", "~/code",
		filepath.Join("/tmp", fmt.Sprintf("claude-%d", os.Getuid()))}
}

func queryGitWorktrees(ctx context.Context) ([]engine.Item, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	return scanWorktrees(ctx, defaultWalker, engine.Host{Home: home}, worktreeRoots(), time.Now())
}

// worktree is one record of `git worktree list --porcelain`.
type worktree struct {
	path, head, branch string // branch is a full ref, empty when detached
	bare, locked       bool
	prunable           bool
}

// scanWorktrees lists every finished linked worktree of the
// repositories found under roots, one item each. The main worktree is
// never an item, and two worktrees of one repository are two items:
// the key is the worktree's path.
func scanWorktrees(ctx context.Context, w *walker, host engine.Host, roots []string, now time.Time) ([]engine.Item, error) {
	repos, partialRoots := discover(ctx, w, host, engine.Discover{Roots: roots, Markers: []string{".git"}, MaxDepth: worktreeRepoDepth})
	var items []engine.Item
	for _, root := range partialRoots {
		items = append(items, unreadableMarker("folders under "+tilde(host, root)))
	}
	seenCommon := map[string]bool{}
	seenMain := map[string]bool{}
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		// Most repositories have no linked worktree; their admin dirs
		// say so without starting git.
		common, ok := commonDir(ctx, w, repo)
		if !ok || seenCommon[common] {
			continue
		}
		seenCommon[common] = true
		if !w.isDir(ctx, filepath.Join(common, "worktrees")) {
			continue
		}
		wts, err := listWorktrees(ctx, repo)
		if err != nil || len(wts) < 2 || seenMain[wts[0].path] {
			continue // not a repository git can read, or no linked worktree
		}
		seenMain[wts[0].path] = true
		found, err := finishedWorktrees(ctx, w, wts[0].path, wts[1:], now)
		items = append(items, found...)
		if err != nil {
			return items, err
		}
	}
	slices.SortFunc(items, func(a, b engine.Item) int { return strings.Compare(a.Path, b.Path) })
	return items, nil
}

// finishedWorktrees checks one repository's linked worktrees, cheapest
// test first: merged (one git call for the repository), then idle
// (a walk of the tree), then clean (a git call per worktree). The only
// error is ctx's.
func finishedWorktrees(ctx context.Context, w *walker, mainPath string, linked []worktree, now time.Time) ([]engine.Item, error) {
	refs, err := readRefs(ctx, mainPath)
	if err != nil {
		return nil, ctx.Err()
	}
	bases := refs.bases()
	if len(bases) == 0 {
		return nil, ctx.Err()
	}
	var candidates []worktree
	var heads []string
	for _, wt := range linked {
		if !wt.bare && !wt.locked && !wt.prunable && wt.head != "" {
			candidates = append(candidates, wt)
			heads = append(heads, wt.head)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	unmerged, err := notReachable(ctx, mainPath, heads, bases)
	if err != nil {
		return nil, ctx.Err()
	}

	var items []engine.Item
	for _, wt := range candidates {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		var why string
		switch {
		case !unmerged[wt.head]:
			why = "merged"
		case wt.branch != "" && refs.gone[wt.branch]:
			why = "upstream gone"
		default:
			continue
		}
		u, found, err := w.usage(ctx, wt.path)
		if err != nil {
			return items, err
		}
		if !found || u.Partial {
			continue // an unread folder could hide recent work
		}
		last := u.Newest
		if t := lastCommitOrCheckout(ctx, w, wt.path); t.After(last) {
			last = t
		}
		if now.Sub(last) < worktreeIdle || !onlyBuildOutput(ctx, wt.path) {
			continue
		}
		name := strings.TrimPrefix(wt.branch, "refs/heads/")
		if name == "" {
			name = "detached " + wt.head[:min(7, len(wt.head))]
		}
		items = append(items, engine.Item{
			Path:     wt.path,
			Label:    name + " (" + why + ")",
			Bytes:    u.Bytes,
			LastUsed: last,
		})
	}
	return items, nil
}

// listWorktrees runs `git worktree list --porcelain -z` in dir; the
// first record is the main worktree.
func listWorktrees(ctx context.Context, dir string) ([]worktree, error) {
	out, err := git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

// parseWorktreeList reads NUL-separated attributes, records ended by an
// empty attribute.
func parseWorktreeList(out []byte) []worktree {
	var wts []worktree
	var cur *worktree
	for _, field := range strings.Split(string(out), "\x00") {
		key, val, _ := strings.Cut(field, " ")
		switch {
		case field == "":
			cur = nil
		case key == "worktree":
			wts = append(wts, worktree{path: val})
			cur = &wts[len(wts)-1]
		case cur == nil:
		case key == "HEAD":
			cur.head = val
		case key == "branch":
			cur.branch = val
		case key == "bare":
			cur.bare = true
		case key == "locked":
			cur.locked = true
		case key == "prunable":
			cur.prunable = true
		}
	}
	return wts
}

// repoRefs is what the merged test needs from a repository's refs.
type repoRefs struct {
	exists   map[string]bool
	originHD string // what refs/remotes/origin/HEAD points at
	gone     map[string]bool
}

func readRefs(ctx context.Context, dir string) (repoRefs, error) {
	out, err := git(ctx, dir, "for-each-ref", "--format=%(refname)%00%(upstream)%00%(upstream:track)%00%(symref)", "refs/heads", "refs/remotes")
	if err != nil {
		return repoRefs{}, err
	}
	r := repoRefs{exists: map[string]bool{}, gone: map[string]bool{}}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 4 {
			continue
		}
		r.exists[f[0]] = true
		if f[1] != "" && f[2] == "[gone]" {
			r.gone[f[0]] = true
		}
		if f[0] == "refs/remotes/origin/HEAD" {
			r.originHD = f[3]
		}
	}
	return r, nil
}

// bases are the refs a merged worktree's HEAD is reachable from: the
// remote's default branch and the usual local and remote main branches.
func (r repoRefs) bases() []string {
	var out []string
	for _, ref := range []string{r.originHD, "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if ref != "" && r.exists[ref] && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// notReachable returns the commits among heads that no base reaches:
// `git rev-list` prints what heads reach and the bases do not, so a
// head missing from its output is merged.
func notReachable(ctx context.Context, dir string, heads, bases []string) (map[string]bool, error) {
	args := append([]string{"rev-list"}, heads...)
	args = append(append(args, "--not"), bases...)
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, h := range heads {
		in[h] = true
	}
	unmerged := map[string]bool{}
	for _, c := range strings.Fields(string(out)) {
		if in[c] {
			unmerged[c] = true
		}
	}
	return unmerged, nil
}

// onlyBuildOutput reports whether `git status --ignored` lists nothing
// but whole ignored directories named in worktreeBuildDirs. Untracked
// and ignored files are shown individually, so they keep the worktree;
// the flags override a user config that would hide them.
func onlyBuildOutput(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "status", "--porcelain", "-z", "--ignored=traditional", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return false
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		path, ignored := strings.CutPrefix(entry, "!! ")
		if !ignored {
			return false
		}
		dirPath, isDir := strings.CutSuffix(path, "/")
		if !isDir || !worktreeBuildDirs[filepath.Base(dirPath)] {
			return false
		}
	}
	return true
}

// lastCommitOrCheckout is when git last moved the worktree's HEAD (a
// commit, checkout or reset), from its admin dir; the working tree's
// own times miss a commit of files edited earlier. The admin dir itself
// and its index are not read: any tool that runs a plain `git status`
// takes index.lock there and may rewrite the index, which was seen
// stamping today's date on worktrees untouched for three weeks. Staged
// changes need no time, since they keep the worktree as unclean. Zero
// when the admin dir cannot be read.
func lastCommitOrCheckout(ctx context.Context, w *walker, wtPath string) time.Time {
	admin, ok := gitdirOf(ctx, w, wtPath)
	if !ok {
		return time.Time{}
	}
	var newest time.Time
	for _, p := range []string{filepath.Join(admin, "HEAD"), filepath.Join(admin, "logs", "HEAD")} {
		if fi, err := w.lstat(ctx, p); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

// gitdirOf reads the "gitdir:" line of dir's .git file: a linked
// worktree's admin dir, or a submodule's git dir.
func gitdirOf(ctx context.Context, w *walker, dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	data, err := bounded(ctx, w, dotGit, func() ([]byte, error) { return os.ReadFile(dotGit) })
	if err != nil {
		return "", false
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	return filepath.Clean(gitdir), true
}

// commonDir finds the repository a .git entry belongs to, as git does:
// a .git directory is its own; a .git file names a git dir, and a
// linked worktree's git dir holds a commondir file naming the shared
// one.
func commonDir(ctx context.Context, w *walker, dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	fi, err := w.lstat(ctx, dotGit)
	if err != nil {
		return "", false
	}
	if fi.IsDir() {
		return dotGit, true
	}
	gitdir, ok := gitdirOf(ctx, w, dir)
	if !ok {
		return "", false
	}
	cf := filepath.Join(gitdir, "commondir")
	data, err := bounded(ctx, w, cf, func() ([]byte, error) { return os.ReadFile(cf) })
	if err != nil {
		return gitdir, true
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	return filepath.Clean(common), true
}

// gitRepoEnv names the variables that point git at a repository; set,
// they would override -C.
var gitRepoEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"}

// git runs a read-only git command in dir. Optional locks are off, so
// `status` never rewrites the index, and fsmonitor is off, so no
// daemon starts.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCallTimeout)
	defer cancel()
	argv := append([]string{"-C", dir, "--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.WaitDelay = toolWaitDelay
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(gitRepoEnv, name)
	})
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return out, nil
}
