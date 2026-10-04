package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lbagic/regrow/internal/engine"
)

const defaultMaxDepth = 6

// builtinExcludes are directory names never descended into during
// discovery, on top of the rule's own excludes. Matched hits are also
// never descended into (a target/ inside a target/ belongs to the
// outer hit).
var builtinExcludes = map[string]bool{
	".git":         true,
	".Trash":       true,
	"Library":      true,
	"node_modules": true,
}

// discover walks the rule's roots looking for directories that match
// the discover spec: base name (if set) plus every marker file
// present. Missing roots are skipped; that lets rules list
// conventional project locations. Hits are sorted and distinct.
// partial lists the roots whose walk met a blocked or unreadable
// directory. Cancelling ctx stops the walk; hits found so far are
// returned.
func discover(ctx context.Context, w *walker, host engine.Host, spec engine.Discover) (hits, partial []string) {
	maxDepth := spec.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	specExclude := make(map[string]bool, len(spec.Exclude))
	for _, name := range spec.Exclude {
		specExclude[name] = true
	}

	// A worker whose directory answered after the walk gave up on it
	// may still call visit; closed keeps it off the returned hits.
	var mu sync.Mutex
	closed := false
	for _, root := range distinctRoots(ctx, w, host, spec.Roots) {
		visit := func(dir string, batch []fs.DirEntry) ([]string, bool) {
			depth := strings.Count(dir[len(root):], string(filepath.Separator)) + 1
			var subdirs, found []string
			for _, e := range batch {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				// Rule excludes win over everything; builtin excludes
				// yield to a name match so rules like node_modules
				// discovery still work.
				if specExclude[name] || depth > maxDepth {
					continue
				}
				p := childPath(dir, name)
				switch {
				case matches(p, name, spec):
					found = append(found, p)
				case !builtinExcludes[name]:
					subdirs = append(subdirs, p)
				}
			}
			if len(found) > 0 {
				mu.Lock()
				if !closed {
					hits = append(hits, found...)
				}
				mu.Unlock()
			}
			return subdirs, true
		}
		incomplete, err := w.walk(ctx, root, visit)
		if err != nil {
			break
		}
		if incomplete {
			partial = append(partial, root)
		}
	}
	mu.Lock()
	closed = true
	out := slices.Clone(hits)
	mu.Unlock()
	slices.Sort(out)
	return slices.Compact(out), partial
}

// distinctRoots expands the rule's roots and keeps the directories
// among them, walked through a symlinked root. A root whose real path
// equals or lies inside another root's is dropped, so no directory is
// reached from two roots.
func distinctRoots(ctx context.Context, w *walker, host engine.Host, raw []string) []string {
	type root struct{ path, real string }
	var kept []root
	for _, r := range raw {
		p := host.ExpandPath(r)
		if !w.isDir(ctx, p) {
			continue
		}
		real, err := bounded(ctx, w, p, func() (string, error) { return filepath.EvalSymlinks(p) })
		if err != nil {
			continue
		}
		covered := false
		for i, k := range kept {
			switch {
			case within(real, k.real):
				covered = true
			case within(k.real, real):
				kept[i] = root{p, real}
				covered = true
			}
		}
		if !covered {
			kept = append(kept, root{p, real})
		}
	}
	var out []string
	for _, k := range kept {
		if !slices.Contains(out, k.path) {
			out = append(out, k.path)
		}
	}
	return out
}

// within reports whether p equals dir or lies below it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

func matches(path, name string, spec engine.Discover) bool {
	if spec.Name != "" && name != spec.Name {
		return false
	}
	for _, marker := range spec.Markers {
		if _, err := os.Stat(filepath.Join(path, marker)); err != nil {
			return false
		}
	}
	return true
}
