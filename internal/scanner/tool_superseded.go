package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Builds a tool keeps beside the one it runs. Codex's app server keeps
// every release it downloaded in releases/<version>-<arch>, and the
// cursor-agent CLI every build in versions/<date>-<sha>; neither
// removes an old one. A link names the live build: Codex's `current`,
// cursor-agent's `cursor-agent` and `agent` in ~/.local/bin. Every other
// build is an item, to the Trash, unless a running process uses it, it
// is newer than the live build (an update waiting), or it changed in
// the last day (an update being written). With a link that leads
// nowhere or outside the folder, or none at all, the live build is
// unknown and nothing is offered. The superseded-build-recheck
// pre-action judges a build again right before the move.

// buildStore is one tool's folder of builds and the links that name
// the live one.
type buildStore struct {
	dir      string
	pointers []string
	tool     string // names the tool in labels and messages
}

func codexReleases(home string) buildStore {
	daemon := filepath.Join(home, ".codex/packages/app-server-daemon")
	return buildStore{dir: filepath.Join(daemon, "releases"), pointers: []string{filepath.Join(daemon, "current")}, tool: "Codex app server"}
}

func cursorAgentVersions(home string) buildStore {
	return buildStore{
		dir:      filepath.Join(home, ".local/share/cursor-agent/versions"),
		pointers: []string{filepath.Join(home, ".local/bin/cursor-agent"), filepath.Join(home, ".local/bin/agent")},
		tool:     "cursor-agent",
	}
}

// buildQuiet is how long a build must go unchanged before it is
// offered.
const buildQuiet = 24 * time.Hour

// supersededBuilds is the query for one store, with its seams: tests
// hand in the walker, the process lists and the clock.
type supersededBuilds struct {
	w      *walker
	store  buildStore
	quiet  time.Duration
	procs  func(ctx context.Context) (processes, error)
	mapped func(ctx context.Context) ([]string, error)
	now    func() time.Time
}

func newSupersededBuilds(store buildStore) *supersededBuilds {
	return &supersededBuilds{w: defaultWalker, store: store, quiet: buildQuiet, procs: runningProcesses, mapped: lsofMapped, now: time.Now}
}

func homeStore(store func(home string) buildStore) ToolQuery {
	return func(ctx context.Context) ([]engine.Item, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		items, _, err := newSupersededBuilds(store(home)).scan(ctx)
		return items, err
	}
}

// RecheckSupersededBuild is the superseded-build-recheck pre-action:
// right before a build goes to the Trash it judges the build again, and
// refuses when it is kept now or the live build cannot be told.
func RecheckSupersededBuild(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	for _, store := range []buildStore{codexReleases(home), cursorAgentVersions(home)} {
		if filepath.Dir(path) == store.dir {
			return newSupersededBuilds(store).recheck(ctx, path)
		}
	}
	return fmt.Errorf("%s is not in a folder of builds regrow knows: not moved", path)
}

// keptBuild is a build the rule leaves alone, and why.
type keptBuild struct{ path, why string }

// scan lists the store's builds: the items offered, and the ones kept,
// each with its reason. A live build that cannot be told offers
// nothing; running processes that cannot be listed are an error.
func (s *supersededBuilds) scan(ctx context.Context) ([]engine.Item, []keptBuild, error) {
	if _, err := s.w.lstat(ctx, s.store.dir); absent(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", s.store.dir, err)
	}
	entries, partial, err := s.w.list(ctx, s.store.dir)
	if err != nil {
		return nil, nil, err
	}
	var items []engine.Item
	if partial {
		items = append(items, unreadableMarker(s.store.tool+" builds under "+homeTilde(s.store.dir)))
	}
	real, targets, why := s.live(ctx)
	live := map[string]bool{}
	for _, t := range targets {
		i := slices.IndexFunc(entries, func(e fs.DirEntry) bool { return e.IsDir() && within(t, filepath.Join(real, e.Name())) })
		if i < 0 {
			why = "a link leads to no build in the folder"
			break
		}
		live[entries[i].Name()] = true
	}
	if why != "" {
		return items, []keptBuild{{s.store.dir, why}}, nil
	}
	var builds []string
	for _, e := range entries {
		if e.IsDir() && !live[e.Name()] {
			builds = append(builds, e.Name())
		}
	}
	if len(builds) == 0 {
		return items, nil, nil
	}
	procs, err := s.procs(ctx)
	if err != nil {
		return nil, nil, err
	}
	mapped, err := s.mapped(ctx)
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(builds)
	var kept []keptBuild
	for _, name := range builds {
		path := filepath.Join(s.store.dir, name)
		item, why, ok, err := s.judge(ctx, name, real, live, procs, mapped)
		switch {
		case err != nil:
			return nil, nil, err
		case !ok:
		case why != "":
			kept = append(kept, keptBuild{path, why})
		default:
			items = append(items, item)
		}
	}
	return items, kept, nil
}

// live reads the links that name the live build: the store's folder as
// links resolve it, and where each link leads. why is set when the live
// build cannot be told.
func (s *supersededBuilds) live(ctx context.Context) (real string, targets []string, why string) {
	real, err := bounded(ctx, s.w, s.store.dir, func() (string, error) { return filepath.EvalSymlinks(s.store.dir) })
	if err != nil {
		return "", nil, "the folder's real path cannot be read"
	}
	for _, p := range s.store.pointers {
		if _, err := s.w.lstat(ctx, p); absent(err) {
			continue
		} else if err != nil {
			return "", nil, homeTilde(p) + " cannot be read"
		}
		target, err := bounded(ctx, s.w, p, func() (string, error) { return filepath.EvalSymlinks(p) })
		if err != nil {
			return "", nil, homeTilde(p) + " leads nowhere"
		}
		if !within(target, real) {
			return "", nil, homeTilde(p) + " leads outside " + homeTilde(s.store.dir)
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return "", nil, "no link names the build in use"
	}
	return real, targets, ""
}

// within reports whether path is dir or inside it, compared folded:
// EvalSymlinks spells a path as the links along it do.
func within(path, dir string) bool {
	p, d := foldName(path), foldName(dir)
	return p == d || strings.HasPrefix(p, d+"/")
}

// judge decides one build: an item, or why it stays. ok is false when
// the build is gone. Its only error is ctx's.
func (s *supersededBuilds) judge(ctx context.Context, name, real string, live map[string]bool, procs processes, mapped []string) (engine.Item, string, bool, error) {
	path := filepath.Join(s.store.dir, name)
	realPath := filepath.Join(real, name)
	if escapedByTools(realPath) || escapedByTools(path) {
		return engine.Item{}, "lsof and ps would not print its path as it is, so no process can be matched to it", true, nil
	}
	if procs.workIn(path) || procs.workIn(realPath) {
		return engine.Item{}, "a running process works in it or names it", true, nil
	}
	for _, m := range mapped {
		if within(m, realPath) || within(m, path) {
			return engine.Item{}, "a running process uses " + m, true, nil
		}
	}
	for l := range live {
		if !newerBuild(name, l) {
			continue
		}
		return engine.Item{}, "it is newer than the build in use, " + l + ": an update may be waiting", true, nil
	}
	u, found, err := s.w.usage(ctx, path)
	switch {
	case err != nil:
		return engine.Item{}, "", false, err
	case !found:
		return engine.Item{}, "", false, nil
	case u.Partial:
		return engine.Item{}, "it cannot all be read", true, nil
	case s.now().Sub(u.Newest) < s.quiet:
		return engine.Item{}, "it changed in the last day: an update may be writing it", true, nil
	}
	return engine.Item{Path: path, Label: s.store.tool + " " + name, Bytes: u.Bytes, LastUsed: u.Newest}, "", true, nil
}

// newerBuild reports whether build's version, the dotted number its
// name starts with, is above the live build's, or cannot be compared
// with it.
func newerBuild(build, live string) bool {
	b, _, _ := strings.Cut(build, "-")
	l, _, _ := strings.Cut(live, "-")
	if !dottedVersionRe.MatchString(b) || !dottedVersionRe.MatchString(l) {
		return true
	}
	return engine.CompareVersion(b, l) > 0
}

// recheck judges one build again, as the pre-action does.
func (s *supersededBuilds) recheck(ctx context.Context, path string) error {
	items, kept, err := s.scan(ctx)
	if err != nil {
		return fmt.Errorf("recheck %s: %w: not moved", path, err)
	}
	for _, it := range items {
		if it.Path == path {
			return nil
		}
	}
	for _, k := range kept {
		if k.path == path || k.path == s.store.dir {
			return fmt.Errorf("%s is kept now (%s): not moved", path, k.why)
		}
	}
	return fmt.Errorf("%s is no longer offered: not moved", path)
}
