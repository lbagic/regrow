package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
// conventional project locations. Results are sorted for determinism.
// Cancelling ctx stops the walk; hits found so far are returned.
func discover(ctx context.Context, host engine.Host, spec engine.Discover) []string {
	maxDepth := spec.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	specExclude := make(map[string]bool, len(spec.Exclude))
	for _, name := range spec.Exclude {
		specExclude[name] = true
	}

	var hits []string
	for _, raw := range spec.Roots {
		root := host.ExpandPath(raw)
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr // cancelled: abort this root's walk
			}
			if err != nil {
				return nil // unreadable: skip, keep walking siblings
			}
			if !d.IsDir() || p == root {
				return nil
			}
			name := d.Name()
			// Rule excludes win over everything; builtin excludes
			// yield to a name match so rules like node_modules
			// discovery still work.
			if specExclude[name] {
				return filepath.SkipDir
			}
			depth := strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator))
			if depth > maxDepth {
				return filepath.SkipDir
			}
			if matches(p, name, spec) {
				hits = append(hits, p)
				return filepath.SkipDir
			}
			if builtinExcludes[name] {
				return filepath.SkipDir
			}
			return nil
		})
	}
	sort.Strings(hits)
	return hits
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
