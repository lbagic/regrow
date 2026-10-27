package scanner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lbagic/regrow/internal/engine"
)

// A rule with unless_running keeps its items while the app or program
// that uses them runs: a cache the app holds open is rebuilt under it,
// or breaks it. The owner's processes are read once per scan, and
// again right before each move.

// runningSet is what the owner's processes execute or map, and where
// they work.
type runningSet struct {
	mapped []string // files a process executes or maps (lsof's txt)
	procs  processes
	w      *walker

	mu      sync.Mutex
	plists  map[string][]string // a bundle's Info.plist strings, by bundle
	plistOK map[string]bool     // whether the bundle has an Info.plist
}

func readRunning(ctx context.Context) (*runningSet, error) {
	mapped, err := lsofMapped(ctx)
	if err != nil {
		return nil, err
	}
	procs, err := runningProcesses(ctx)
	if err != nil {
		return nil, err
	}
	return &runningSet{mapped: mapped, procs: procs, w: defaultWalker}, nil
}

// keeps says why the guard keeps path, or "" when nothing running uses
// it. An app bundle a process maps whose Info.plist cannot be read is
// an error: the app it holds cannot be told.
func (s *runningSet) keeps(ctx context.Context, u engine.UnlessRunning, path string) (string, error) {
	for _, m := range s.mapped {
		name := filepath.Base(m)
		if slices.ContainsFunc(u.Commands, func(c string) bool { return strings.EqualFold(c, name) }) {
			return "`" + name + "` is running", nil
		}
	}
	if len(u.Apps) > 0 {
		for _, bundle := range s.bundles() {
			texts, err := s.infoPlist(ctx, bundle)
			if err != nil {
				return "", err
			}
			for _, id := range u.Apps {
				if slices.ContainsFunc(texts, func(t string) bool { return strings.EqualFold(t, id) }) {
					return "the app " + id + " is running (" + bundle + ")", nil
				}
			}
		}
	}
	if u.Inside && path != "" {
		return s.inside(ctx, path)
	}
	return "", nil
}

// inside says why a process keeps path: it works in it, names it, or
// executes or maps a file from it, spelled as the rule spells it or as
// links resolve it, which is how lsof prints it.
func (s *runningSet) inside(ctx context.Context, path string) (string, error) {
	real, err := bounded(ctx, s.w, path, func() (string, error) { return filepath.EvalSymlinks(path) })
	switch {
	case absent(err):
		real = path
	case err != nil:
		return "its real path cannot be read, so no process can be matched to it", nil
	}
	if escapedByTools(path) || escapedByTools(real) {
		return "lsof and ps would not print its path as it is, so no process can be matched to it", nil
	}
	for _, p := range []string{path, real} {
		if s.procs.workIn(p) {
			return "a running process works in it or names it", nil
		}
		if slices.ContainsFunc(s.mapped, func(m string) bool { return within(m, p) }) {
			return "a running process executes or maps a file from it", nil
		}
	}
	return "", nil
}

// bundles are the folders before each /Contents/ of each mapped file:
// the app bundle a process runs from, a helper's included, and the app
// whose bundle holds that one.
func (s *runningSet) bundles() []string {
	var out []string
	for _, m := range s.mapped {
		for i := 0; ; {
			j := strings.Index(m[i:], "/Contents/")
			if j < 0 {
				break
			}
			if b := m[:i+j]; b != "" && !slices.Contains(out, b) {
				out = append(out, b)
			}
			i += j + len("/Contents")
		}
	}
	return out
}

// infoPlist reads a bundle's Info.plist strings once. A bundle with no
// Info.plist is not an app, and holds none.
func (s *runningSet) infoPlist(ctx context.Context, bundle string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plistOK == nil {
		s.plists, s.plistOK = map[string][]string{}, map[string]bool{}
	}
	if _, read := s.plistOK[bundle]; read {
		return s.plists[bundle], nil
	}
	if strings.Contains(bundle, `\`) {
		return nil, fmt.Errorf("lsof escaped an app's path (%s), so the app cannot be told", bundle)
	}
	p := filepath.Join(bundle, "Contents", "Info.plist")
	data, err := bounded(ctx, s.w, p, func() ([]byte, error) { return readLimited(p, maxInfoPlist) })
	switch {
	case absent(err):
		s.plistOK[bundle] = false
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	texts, err := plistTexts(data)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	s.plists[bundle], s.plistOK[bundle] = texts, true
	return texts, nil
}

// runningOnce reads the processes for one scan, at the first guarded
// rule that has items.
type runningOnce struct {
	once sync.Once
	read func(context.Context) (*runningSet, error)
	set  *runningSet
	err  error
}

func (o *runningOnce) get(ctx context.Context) (*runningSet, error) {
	o.once.Do(func() { o.set, o.err = o.read(ctx) })
	return o.set, o.err
}

// guard drops the items a guarded rule keeps now. With the processes
// unknown it offers nothing.
func guard(ctx context.Context, r engine.Rule, items []engine.Item, running *runningOnce) ([]engine.Item, error) {
	if r.UnlessRunning == nil || len(items) == 0 {
		return items, nil
	}
	set, err := running.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("kept while the running processes cannot be read: %w", err)
	}
	var out []engine.Item
	for _, it := range items {
		why, err := set.keeps(ctx, *r.UnlessRunning, it.Path)
		if err != nil {
			return nil, err
		}
		if why == "" {
			out = append(out, it)
		}
	}
	return out, nil
}

// RecheckRunning is the running-recheck pre-action: right before a
// guarded rule's item goes to the Trash, it reads the processes again
// and refuses while the rule keeps the item, or when they cannot be
// read.
func RecheckRunning(ctx context.Context, r engine.Rule, path string) error {
	return recheckRunning(ctx, r, path, readRunning)
}

func recheckRunning(ctx context.Context, r engine.Rule, path string, read func(context.Context) (*runningSet, error)) error {
	if r.UnlessRunning == nil {
		return errors.New(r.ID + " names nothing to check as running")
	}
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	set, err := read(ctx)
	if err != nil {
		return fmt.Errorf("kept while the running processes cannot be read: %w", err)
	}
	why, err := set.keeps(ctx, *r.UnlessRunning, path)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New("kept: " + why)
	}
	return nil
}
