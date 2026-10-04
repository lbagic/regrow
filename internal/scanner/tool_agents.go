package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Claude Code gives every session a scratch directory,
// <tmp>/claude-<uid>/<project>/<session-id>/, and nothing clears it
// before a reboot. The scratch of an ended session is offered to the
// Trash (agent-scratch); every other session is kept, with the reason
// in its row (agent-scratch-kept).
//
// A session is live while a process runs it, or while its transcript
// or its scratch changed within agentWindow. A session whose scratch
// holds either end of a git worktree link that leaves it (a worktree
// of a repository elsewhere, or a repository with a worktree
// elsewhere) is kept too: worktrees are the git-worktrees rule's to
// remove, and trashing one end breaks the other.

// agentWindow is how recent a change to a session's transcript or
// scratch keeps it live without a process.
const agentWindow = 24 * time.Hour

var windowText = strconv.Itoa(int(agentWindow.Hours())) + "h"

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// agentSessions owns one scan's view of the agent sessions: listed
// once, read by both rules. The zero value reads the real machine;
// tests inject the root, both listers and the clock.
type agentSessions struct {
	// Root holds one directory per project, each holding one scratch
	// directory per session; "" means <tmp>/claude-<uid>.
	Root string
	// Running returns the ids of sessions a running process belongs
	// to; nil reads Claude Code's session registry.
	Running func(ctx context.Context) (map[string]bool, error)
	// Activity returns the newest change to a session's transcript,
	// zero when it has none; nil reads Claude Code's projects folder.
	Activity func(ctx context.Context, project, id string) time.Time
	Now      func() time.Time
	w        *walker

	mu    sync.Mutex
	done  bool
	ended []engine.Item
	kept  []engine.Item
	err   error
}

// Ended is the agent-scratch query: sessions nothing keeps.
func (a *agentSessions) Ended(ctx context.Context) ([]engine.Item, error) {
	a.load(ctx)
	return a.ended, a.err
}

// Kept is the agent-scratch-kept query: every other session.
func (a *agentSessions) Kept(ctx context.Context) ([]engine.Item, error) {
	a.load(ctx)
	return a.kept, a.err
}

func (a *agentSessions) load(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	a.ended, a.kept, a.err = a.list(ctx)
}

func (a *agentSessions) walker() *walker {
	if a.w != nil {
		return a.w
	}
	return defaultWalker
}

type agentSession struct{ project, id, path string }

func (a *agentSessions) list(ctx context.Context) (ended, kept []engine.Item, err error) {
	w := a.walker()
	root := a.Root
	if root == "" {
		root = agentScratchRoot()
	}
	if root == "" || !w.isDir(ctx, root) {
		return nil, nil, nil
	}
	sessions, partial, err := listSessions(ctx, w, root)
	if err != nil {
		return nil, nil, err
	}
	if partial {
		kept = append(kept, unreadableMarker("sessions under "+root))
	}
	if len(sessions) == 0 {
		return nil, kept, nil
	}

	runningFn, activity, now := a.Running, a.Activity, time.Now
	if runningFn == nil {
		runningFn = func(ctx context.Context) (map[string]bool, error) {
			return claudeRunning(ctx, w, claudeConfigDir(), psCommands)
		}
	}
	if activity == nil {
		activity = func(ctx context.Context, project, id string) time.Time {
			return claudeActivity(ctx, w, claudeConfigDir(), project, id)
		}
	}
	if a.Now != nil {
		now = a.Now
	}
	running, err := runningFn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot tell which agent sessions are running: %w", err)
	}

	for _, s := range sessions {
		u, links, err := measureSession(ctx, w, s.path)
		if err != nil {
			return nil, nil, err
		}
		changed := activity(ctx, s.project, s.id)
		reason := ""
		switch {
		case running[s.id]:
			reason = "running"
		case now().Sub(changed) < agentWindow:
			reason = "transcript changed within " + windowText
		case now().Sub(u.Newest) < agentWindow:
			reason = "scratch changed within " + windowText
		case u.Partial:
			reason = "not fully readable, so git worktrees inside cannot be ruled out"
		default:
			reason = worktreeReason(linkedOutside(ctx, w, s.path, links))
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		item := engine.Item{
			Path:     s.path,
			Label:    "session " + s.id[:8] + " · " + s.project,
			Arg:      s.id,
			Bytes:    u.Bytes,
			LastUsed: u.Newest,
			Partial:  u.Partial,
		}
		if changed.After(item.LastUsed) {
			item.LastUsed = changed
		}
		if reason == "" {
			ended = append(ended, item)
			continue
		}
		item.Label += " — " + reason
		kept = append(kept, item)
	}
	return ended, kept, nil
}

// listSessions finds <root>/<project>/<session-id> directories, sorted
// by path. Anything else under the root is not a session's and is left
// out. partial: a folder on the way refused or blocked.
func listSessions(ctx context.Context, w *walker, root string) (sessions []agentSession, partial bool, err error) {
	projects, partial, err := w.list(ctx, root)
	if err != nil {
		return nil, true, err
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		entries, incomplete, err := w.list(ctx, dir)
		if err != nil {
			return nil, true, err
		}
		partial = partial || incomplete
		for _, e := range entries {
			if e.IsDir() && sessionIDRe.MatchString(e.Name()) {
				sessions = append(sessions, agentSession{p.Name(), e.Name(), filepath.Join(dir, e.Name())})
			}
		}
	}
	slices.SortFunc(sessions, func(a, b agentSession) int { return strings.Compare(a.path, b.path) })
	return sessions, partial, nil
}

// gitLinks are the files that tie a checkout to its repository: a
// checkout's .git file names the repository's git directory, and the
// repository's worktrees/<name>/gitdir names the checkout's .git file.
type gitLinks struct{ checkouts, registrations []string }

// measureSession sizes a session's scratch and collects its git links.
func measureSession(ctx context.Context, w *walker, path string) (Usage, gitLinks, error) {
	var mu sync.Mutex
	closed := false
	var links gitLinks
	u, _, err := w.usageSeeing(ctx, path, func(dir string, e fs.DirEntry) {
		if !e.Type().IsRegular() {
			return
		}
		var list *[]string
		switch {
		case e.Name() == ".git":
			list = &links.checkouts
		case e.Name() == "gitdir" && filepath.Base(filepath.Dir(dir)) == "worktrees":
			list = &links.registrations
		default:
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !closed {
			*list = append(*list, childPath(dir, e.Name()))
		}
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	slices.Sort(links.checkouts)
	slices.Sort(links.registrations)
	return u, links, err
}

// linkedOutside describes each git link that crosses the session's
// edge to something that still exists: a worktree whose repository
// lives elsewhere, or a repository with a worktree checked out
// elsewhere. Trashing either end breaks the other. A link that cannot
// be read counts.
func linkedOutside(ctx context.Context, w *walker, session string, links gitLinks) []string {
	var out []string
	for _, f := range links.checkouts {
		if crossesOut(ctx, w, session, f, "gitdir:") {
			out = append(out, "git worktree "+relTo(session, filepath.Dir(f)))
		}
	}
	for _, f := range links.registrations {
		if crossesOut(ctx, w, session, f, "") {
			repo := filepath.Dir(filepath.Dir(filepath.Dir(f)))
			if filepath.Base(repo) == ".git" {
				repo = filepath.Dir(repo)
			}
			out = append(out, "repository "+relTo(session, repo)+" with a worktree elsewhere")
		}
	}
	return out
}

// crossesOut reports whether the link file points outside the session
// at something that exists. The target is the file's first line, after
// prefix; relative targets resolve against the file's folder. A file
// without a target is not a link.
func crossesOut(ctx context.Context, w *walker, session, file, prefix string) bool {
	data, err := bounded(ctx, w, file, func() ([]byte, error) {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return io.ReadAll(io.LimitReader(f, 4096))
	})
	if err != nil {
		return true
	}
	line, _, _ := strings.Cut(string(data), "\n")
	target, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
	target = strings.TrimSpace(target)
	if !ok || target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(file), target)
	}
	if strings.HasPrefix(filepath.Clean(target), session+string(filepath.Separator)) {
		return false
	}
	_, err = w.stat(ctx, target)
	return err == nil || !absent(err)
}

func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil {
		return rel
	}
	return path
}

func worktreeReason(links []string) string {
	switch len(links) {
	case 0:
		return ""
	case 1:
		return "holds " + links[0] + "; remove the worktree first"
	}
	return fmt.Sprintf("holds %s and %d more; remove the worktrees first", links[0], len(links)-1)
}

// agentScratchRoot is where Claude Code keeps session scratch on
// macOS; "" elsewhere, where the layout is unverified.
func agentScratchRoot() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	return filepath.Join("/private/tmp", "claude-"+strconv.Itoa(os.Getuid()))
}

func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// claudeActivity is the newest change to a session's transcript: the
// <id>.jsonl file and the <id>/ folder beside it (subagent transcripts,
// tool results).
func claudeActivity(ctx context.Context, w *walker, config, project, id string) time.Time {
	if config == "" {
		return time.Time{}
	}
	dir := filepath.Join(config, "projects", project)
	var newest time.Time
	if fi, err := w.lstat(ctx, filepath.Join(dir, id+".jsonl")); err == nil {
		newest = fi.ModTime()
	}
	if u, found, _ := w.usage(ctx, filepath.Join(dir, id)); found && u.Newest.After(newest) {
		newest = u.Newest
	}
	return newest
}

// claudeRunning reads Claude Code's session registry: every running
// claude process keeps <config>/sessions/<pid>.json naming its
// session, and a registry file outlives a crashed process, so the pid
// must still be running. procs lists running processes by pid. If
// claude processes run and the registry accounts for none of them, the
// registry is not what this code expects, and no session can be shown
// to have ended.
func claudeRunning(ctx context.Context, w *walker, config string, procs func(context.Context) (map[int]string, error)) (map[string]bool, error) {
	if config == "" {
		return nil, errors.New("no Claude Code config folder")
	}
	pids, err := procs(ctx)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(config, "sessions")
	entries, partial, err := w.list(ctx, dir)
	if err != nil {
		return nil, err
	}
	if partial {
		return nil, fmt.Errorf("session registry %s is unreadable", dir)
	}
	live := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := bounded(ctx, w, path, func() ([]byte, error) { return os.ReadFile(path) })
		if err != nil {
			continue
		}
		var reg struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &reg) != nil || reg.SessionID == "" {
			continue
		}
		if _, ok := pids[reg.PID]; ok {
			live[reg.SessionID] = true
		}
	}
	claudes := 0
	for _, name := range pids {
		if filepath.Base(name) == "claude" {
			claudes++
		}
	}
	if claudes > 0 && len(live) == 0 {
		return nil, fmt.Errorf("%d claude processes are running and the session registry %s names none of them", claudes, dir)
	}
	return live, nil
}

// psCommands lists running processes: pid to command name.
func psCommands(ctx context.Context) (map[int]string, error) {
	out, found, err := runTool(ctx, "ps", "-axo", "pid=,comm=")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("ps is not available")
	}
	return parsePS(string(out)), nil
}

func parsePS(out string) map[int]string {
	procs := map[int]string{}
	for line := range strings.SplitSeq(out, "\n") {
		pid, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			procs[n] = strings.TrimSpace(name)
		}
	}
	return procs
}
