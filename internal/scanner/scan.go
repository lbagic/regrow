package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/lbagic/regrow/internal/engine"
)

// Scanner measures every rule against a host. Host and Queries are
// injectable so tests run against fixture homes and fake tools.
type Scanner struct {
	Host    engine.Host
	Queries map[string]ToolQuery
}

// New builds a scanner for the host with the built-in tool queries.
func New(host engine.Host) *Scanner {
	return &Scanner{Host: host, Queries: DefaultQueries()}
}

// Scan measures all rules and returns one finding per rule, catalog
// order preserved. Rules whose targets are absent return a finding
// with no items; scan failures land in Finding.Err instead of
// aborting the run.
func (s *Scanner) Scan(ctx context.Context, rules []engine.Rule) []engine.Finding {
	findings := make([]engine.Finding, len(rules))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, r := range rules {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			findings[i] = s.scanRule(ctx, r)
		}()
	}
	wg.Wait()
	return findings
}

func (s *Scanner) scanRule(ctx context.Context, r engine.Rule) engine.Finding {
	f := engine.Finding{Rule: r}

	for _, path := range s.Host.ResolvePaths(r) {
		for _, p := range expandGlob(path) {
			item, ok, err := s.measure(ctx, p)
			if err != nil {
				f.Err = joinErr(f.Err, err)
				continue
			}
			if ok {
				f.Items = append(f.Items, item)
			}
		}
	}

	if r.Discover != nil {
		for _, path := range discover(ctx, s.Host, *r.Discover) {
			item, ok, err := s.measure(ctx, path)
			if err != nil {
				f.Err = joinErr(f.Err, err)
				continue
			}
			if ok {
				f.Items = append(f.Items, item)
			}
		}
	}

	if r.ToolQuery != "" {
		query, known := s.Queries[r.ToolQuery]
		if !known {
			f.Err = joinErr(f.Err, fmt.Errorf("unknown tool query %q", r.ToolQuery))
		} else if items, err := query(ctx); err != nil {
			f.Err = joinErr(f.Err, err)
		} else {
			f.Items = append(f.Items, items...)
		}
	}

	// Every item leaves the scanner with its stable key (Prompt G0):
	// "ruleID/key" is how selection atoms and --json address it.
	f.FillItemKeys(s.Host.Home)
	return f
}

// measure stats and sizes one path. Absent paths are not an error:
// most rules simply do not apply to a given machine.
func (s *Scanner) measure(ctx context.Context, path string) (engine.Item, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return engine.Item{}, false, nil
	}
	if err != nil {
		return engine.Item{}, false, err
	}
	bytes, err := DirSize(ctx, path)
	if err != nil {
		return engine.Item{}, false, err
	}
	return engine.Item{Path: path, Bytes: bytes, LastUsed: info.ModTime()}, true, nil
}

// expandGlob resolves glob metacharacters against the filesystem so
// rules can target patterns like /Applications/Install macOS*.app.
// Literal paths pass through untouched; a pattern with no matches
// yields nothing — the target is simply absent on this machine.
func expandGlob(path string) []string {
	if !strings.ContainsAny(path, "*?[") {
		return []string{path}
	}
	matches, err := filepath.Glob(path) // returns sorted matches
	if err != nil {
		return nil
	}
	return matches
}

func joinErr(existing string, err error) string {
	if existing == "" {
		return err.Error()
	}
	return existing + "; " + err.Error()
}
