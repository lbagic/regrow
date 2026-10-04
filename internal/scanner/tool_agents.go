package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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
// A session is live while a process runs it or works inside its
// scratch, or while its transcript or its scratch changed within
// agentWindow. A session whose scratch holds either end of a git
// worktree link that leaves it (a worktree of a repository elsewhere,
// or a repository with a worktree elsewhere) is kept too: worktrees are
// the git-worktrees rule's to remove, and trashing one end breaks the
// other. Whatever cannot be read in full is kept.

// agentWindow is how recent a change to a session's transcript or
// scratch keeps it live without a process.
const agentWindow = 24 * time.Hour

var windowText = strconv.Itoa(int(agentWindow.Hours())) + "h"

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// liveness is what the running processes say about the sessions.
type liveness struct {
	// sessions holds the ids of sessions a running process belongs to.
	sessions map[string]bool
	// paths are where running processes work: each registered
	// session's working directory and every process's executable.
	paths []string
}

// inside reports whether a running process works in or below dir.
func (l liveness) inside(dir string) bool {
	for _, p := range l.paths {
		if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// agentSessions owns one scan's view of the agent sessions: listed
// once, read by both rules. The zero value reads the real machine;
// tests inject the root, both listers and the clock.
type agentSessions struct {
	// Root holds one directory per project, each holding one scratch
	// directory per session; "" means <tmp>/claude-<uid>.
	Root string
	// Live reports what running processes keep; nil reads Claude
	// Code's session registries and ps.
	Live func(ctx context.Context) (liveness, error)
	// Activity returns the newest change to a session's transcript,
	// zero when it has none, and whether all of it could be read; nil
	// reads Claude Code's projects folder.
	Activity func(ctx context.Context, project, id string) (newest time.Time, complete bool)
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

// RecheckAgentScratch is the agent-scratch pre-action: right before a
// session's scratch goes to the Trash it judges the session again and
// refuses when anything keeps it now. The scan may be hours old, and
// the session may have been resumed since.
func RecheckAgentScratch(ctx context.Context, path string) error {
	return (&agentSessions{}).recheck(ctx, path)
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

func (a *agentSessions) live(ctx context.Context) (liveness, error) {
	if a.Live != nil {
		return a.Live(ctx)
	}
	return claudeLiveness(ctx, a.walker(), claudeConfigDir(), psCommands, psConfigDir)
}

func (a *agentSessions) activity(ctx context.Context, project, id string) (time.Time, bool) {
	if a.Activity != nil {
		return a.Activity(ctx, project, id)
	}
	return claudeActivity(ctx, a.walker(), claudeConfigDir(), project, id)
}

func (a *agentSessions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
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
	// Items carry the real path: git reports worktrees by real path,
	// and the containment forest compares paths as written.
	given := root
	root, err = bounded(ctx, w, given, func() (string, error) { return filepath.EvalSymlinks(given) })
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %s: %w", given, err)
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
	live, err := a.live(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot tell which agent sessions are running: %w", err)
	}
	for _, s := range sessions {
		item, reason, found, err := a.judge(ctx, s, live)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue
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

// judge measures one session and says why it is kept; "" means it has
// ended. found is false when its scratch is gone. The item is keyed by
// path: a session id can own scratch under more than one project.
func (a *agentSessions) judge(ctx context.Context, s agentSession, live liveness) (item engine.Item, reason string, found bool, err error) {
	w := a.walker()
	u, links, found, err := measureSession(ctx, w, s.path)
	if err != nil || !found {
		return engine.Item{}, "", found, err
	}
	changed, complete := a.activity(ctx, s.project, s.id)
	now := a.now()
	switch {
	case live.sessions[s.id]:
		reason = "running"
	case live.inside(s.path):
		reason = "a running process works inside it"
	case now.Sub(changed) < agentWindow:
		reason = "transcript changed within " + windowText
	case now.Sub(u.Newest) < agentWindow:
		reason = "scratch changed within " + windowText
	case !complete:
		reason = "transcript not fully readable"
	case u.Partial:
		reason = "not fully readable, so git worktrees inside cannot be ruled out"
	default:
		reason = worktreeReason(linkedOutside(ctx, w, s.path, links))
	}
	if err := ctx.Err(); err != nil {
		return engine.Item{}, "", false, err
	}
	item = engine.Item{
		Path:     s.path,
		Label:    "session " + s.id[:8] + " · " + s.project,
		Bytes:    u.Bytes,
		LastUsed: u.Newest,
		Partial:  u.Partial,
	}
	if changed.After(item.LastUsed) {
		item.LastUsed = changed
	}
	return item, reason, true, nil
}

func (a *agentSessions) recheck(ctx context.Context, path string) error {
	id := filepath.Base(path)
	if !sessionIDRe.MatchString(id) {
		return fmt.Errorf("%s is not an agent session's scratch", path)
	}
	live, err := a.live(ctx)
	if err != nil {
		return fmt.Errorf("cannot tell whether session %s is running: %w", id[:8], err)
	}
	_, reason, found, err := a.judge(ctx, agentSession{filepath.Base(filepath.Dir(path)), id, path}, live)
	switch {
	case err != nil:
		return err
	case !found:
		return fmt.Errorf("%s is gone", path)
	case reason != "":
		return fmt.Errorf("session %s is kept now: %s", id[:8], reason)
	}
	return nil
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
// found is false when the scratch no longer exists.
func measureSession(ctx context.Context, w *walker, path string) (Usage, gitLinks, bool, error) {
	var mu sync.Mutex
	closed := false
	var links gitLinks
	u, found, err := w.usageSeeing(ctx, path, func(dir string, e fs.DirEntry) {
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
	return u, links, found, err
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
// at something that exists, comparing real paths. The target is the
// file's first line, after prefix; relative targets resolve against the
// file's folder. A file without a target is not a link.
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
	real, err := bounded(ctx, w, target, func() (string, error) { return filepath.EvalSymlinks(target) })
	if err != nil {
		return !absent(err)
	}
	return !strings.HasPrefix(real, session+string(filepath.Separator))
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
// tool results). complete is false when any of it could not be read.
func claudeActivity(ctx context.Context, w *walker, config, project, id string) (newest time.Time, complete bool) {
	if config == "" {
		return time.Time{}, false
	}
	dir := filepath.Join(config, "projects", project)
	complete = true
	switch fi, err := w.lstat(ctx, filepath.Join(dir, id+".jsonl")); {
	case err == nil:
		newest = fi.ModTime()
	case !absent(err):
		complete = false
	}
	if u, found, _ := w.usage(ctx, filepath.Join(dir, id)); found {
		if u.Newest.After(newest) {
			newest = u.Newest
		}
		complete = complete && !u.Partial
	}
	return newest, complete
}

// registration is one file of Claude Code's session registry.
type registration struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
}

// claudeLiveness reads Claude Code's session registries: every running
// claude process keeps <config>/sessions/<pid>.json, naming its session
// and working directory, under the config folder it runs with. A
// registry file outlives a crashed process, so only running pids count.
// When a running claude process is in no registry, or its registry file
// cannot be read, no session can be shown to have ended: an error.
// procs lists the user's running processes, pid to executable; configOf
// returns the config folder a process runs with, "" when unknown.
func claudeLiveness(ctx context.Context, w *walker, config string, procs func(context.Context) (map[int]string, error), configOf func(context.Context, int) string) (liveness, error) {
	pids, err := procs(ctx)
	if err != nil {
		return liveness{}, err
	}
	reg := map[int]registration{}
	read := map[string]bool{}
	readConfig := func(dir string) error {
		if dir == "" || read[filepath.Clean(dir)] {
			return nil
		}
		read[filepath.Clean(dir)] = true
		return readRegistry(ctx, w, filepath.Join(dir, "sessions"), pids, reg)
	}
	if err := readConfig(config); err != nil {
		return liveness{}, err
	}
	for _, pid := range slices.Sorted(maps.Keys(pids)) {
		if filepath.Base(pids[pid]) != "claude" {
			continue
		}
		if _, ok := reg[pid]; ok {
			continue
		}
		if err := readConfig(configOf(ctx, pid)); err != nil {
			return liveness{}, err
		}
		if _, ok := reg[pid]; !ok {
			return liveness{}, fmt.Errorf("claude process %d is in no session registry", pid)
		}
	}
	live := liveness{sessions: map[string]bool{}}
	for _, r := range reg {
		live.sessions[r.SessionID] = true
		if filepath.IsAbs(r.Cwd) {
			live.paths = append(live.paths, scanSpelling(r.Cwd))
		}
	}
	for _, exe := range pids {
		if filepath.IsAbs(exe) {
			live.paths = append(live.paths, scanSpelling(exe))
		}
	}
	slices.Sort(live.paths)
	return live, nil
}

// readRegistry adds the registry files of running pids in dir to reg. A
// missing folder holds none. A folder that cannot be listed, or a
// running pid's file that cannot be read or parsed (files are rewritten
// in place), is an error.
func readRegistry(ctx context.Context, w *walker, dir string, pids map[int]string, reg map[int]registration) error {
	entries, partial, err := w.list(ctx, dir)
	if err != nil {
		return err
	}
	if partial {
		return fmt.Errorf("session registry %s is unreadable", dir)
	}
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(stem)
		if err != nil {
			continue
		}
		if _, running := pids[pid]; !running {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := bounded(ctx, w, path, func() ([]byte, error) { return os.ReadFile(path) })
		var r registration
		if err == nil {
			err = json.Unmarshal(data, &r)
		}
		if err == nil && r.SessionID == "" {
			err = errors.New("no sessionId")
		}
		if err != nil {
			return fmt.Errorf("registry file %s of running process %d: %w", path, pid, err)
		}
		reg[pid] = r
	}
	return nil
}

// scanSpelling spells a path the way the scan does: on macOS /tmp,
// /var and /etc are symlinks into /private.
func scanSpelling(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "darwin" {
		for _, link := range []string{"/tmp", "/var", "/etc"} {
			if p == link || strings.HasPrefix(p, link+"/") {
				return "/private" + p
			}
		}
	}
	return p
}

// psCommands lists the user's running processes: pid to executable.
func psCommands(ctx context.Context) (map[int]string, error) {
	out, found, err := runTool(ctx, "ps", "-x", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,comm=")
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

var configDirRe = regexp.MustCompile(`(?:^|\s)CLAUDE_CONFIG_DIR=(\S+)`)

// psConfigDir reads CLAUDE_CONFIG_DIR from a process's environment,
// which ps shows for the user's own processes. A value with spaces is
// cut short and its registry not found, which reads as unknown.
func psConfigDir(ctx context.Context, pid int) string {
	out, found, err := runTool(ctx, "ps", "-ww", "-E", "-o", "command=", "-p", strconv.Itoa(pid))
	if err != nil || !found {
		return ""
	}
	return parseConfigDir(string(out))
}

func parseConfigDir(env string) string {
	m := configDirRe.FindStringSubmatch(env)
	if m == nil || !filepath.IsAbs(m[1]) {
		return ""
	}
	return m[1]
}
