package scanner

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// CauseQuery says whether one fix-the-cause row is in effect on this
// machine, for the rule that declares it. It only reads: settings
// files, directory listings, `go env`. detail is one line, as
// engine.CauseCheck.Detail describes. Checks live in code, as tool
// queries do; rules reference them by name via causes[].check.
type CauseQuery func(ctx context.Context, r engine.Rule) (verdict engine.Verdict, detail string)

// DefaultCauseQueries returns the built-in checks for the host.
func DefaultCauseQueries(host engine.Host) map[string]CauseQuery {
	return (&causeChecks{host: host}).queries()
}

func (c *causeChecks) queries() map[string]CauseQuery {
	return map[string]CauseQuery{
		"aerial-downloads":         c.aerialDownloads,
		"docker-build-cache-limit": c.dockerBuildCacheLimit,
		"docker-vm-memory":         c.dockerVMMemory,
		"go-worktrees-trimpath":    c.goWorktreesTrimpath,
	}
}

// Causes runs the fix-the-cause checks the rules declare for this
// host, catalog order kept. A check that the scanner does not know, or
// that gives no answer by the query deadline, is unknown.
func (s *Scanner) Causes(ctx context.Context, rules []engine.Rule) []engine.CauseCheck {
	type answer struct {
		verdict engine.Verdict
		detail  string
	}
	var out []engine.CauseCheck
	for _, r := range rules {
		for _, c := range s.Host.Causes(r) {
			row := engine.CauseCheck{RuleID: r.ID, Cause: c, Verdict: engine.VerdictUnknown}
			query, known := s.CauseQueries[c.Check]
			if !known {
				row.Detail = fmt.Sprintf("unknown cause check %q", c.Check)
				out = append(out, row)
				continue
			}
			a, err := withDeadline(ctx, s.timeout(), func(ctx context.Context) (answer, error) {
				v, d := query(ctx, r)
				return answer{v, d}, nil
			})
			if err != nil {
				row.Detail = err.Error()
			} else {
				row.Verdict, row.Detail = a.verdict, a.detail
			}
			out = append(out, row)
		}
	}
	return out
}

// causeChecks is what the built-in checks read. A nil seam reads the
// real machine; tests point the seams at a fixture.
type causeChecks struct {
	host engine.Host
	w    *walker
	now  func() time.Time
	// goEnv answers `go env <key>`; nil asks the go command on PATH.
	goEnv func(ctx context.Context, key string) (string, error)
	// memory is the machine's physical memory in bytes, 0 when unknown.
	memory func() int64
}

func (c *causeChecks) walker() *walker {
	if c.w != nil {
		return c.w
	}
	return defaultWalker
}

func (c *causeChecks) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// settingsLimit bounds one settings file read; the files read here are
// a few hundred bytes.
const settingsLimit = 1 << 20

// read returns a small file's content through the walker's bound: a
// settings file inside another app's container can block like any
// other path there.
func (c *causeChecks) read(ctx context.Context, path string) ([]byte, error) {
	return bounded(ctx, c.walker(), path, func() ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return io.ReadAll(io.LimitReader(f, settingsLimit))
	})
}

// agoText renders how long ago something happened, coarsely.
func agoText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d d ago", int(d.Hours()/24))
	}
}
