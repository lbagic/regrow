package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/autopilot"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/tui"
)

// interspersedArgs keeps parsing flags after the first name, so both
// `regrow prune go-build --yes` and `regrow prune --yes go-build`
// work. fs has already parsed the flags before the name.
func interspersedArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var names []string
	for len(args) > 0 {
		names, args = append(names, args[0]), args[1:]
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
	}
	return names, nil
}

// runPrune trims a cache under its rule's prune policy. Without --yes
// it only plans: nothing is locked, journaled or deleted.
func runPrune(host engine.Host, catalog []engine.Rule, names []string, opts options) error {
	known := strings.Join(autopilot.Names(catalog), ", ")
	if len(names) != 1 {
		return fmt.Errorf("usage: regrow prune <cache> [--yes] (caches with a prune policy: %s)", known)
	}
	rule, ok := autopilot.FindRule(catalog, names[0])
	if !ok {
		return fmt.Errorf("no prune policy for %q (caches with one: %s)", names[0], known)
	}
	ap, err := autopilot.New(host, catalog)
	if err != nil {
		return err
	}

	if !opts.yes {
		preview, err := ap.Preview(context.Background(), rule)
		if err != nil {
			return err
		}
		if opts.asJSON {
			return emitJSON(preview)
		}
		for _, line := range previewLines(rule, preview) {
			fmt.Println(line)
		}
		return nil
	}

	res, err := ap.Prune(context.Background(), rule)
	if opts.asJSON {
		if jerr := emitJSON(res); jerr != nil {
			return jerr
		}
	} else {
		for _, line := range pruneResultLines(res) {
			fmt.Println(line)
		}
	}
	if err == nil && res.Error != "" {
		err = fmt.Errorf("prune %s failed: %s", rule.ID, res.Error)
	}
	return err
}

func previewLines(r engine.Rule, p autopilot.Preview) []string {
	var lines []string
	s := p.Survey
	if s.Cache != "" {
		lines = append(lines,
			fmt.Sprintf("%s  %s", r.Title, s.Cache),
			fmt.Sprintf("  now     %s in %d entries", tui.HumanBytes(s.CacheBytes), s.CacheFiles),
			fmt.Sprintf("  policy  keep every entry used in the last %s; trim the rest, oldest first, down to %s",
				r.Prune.MinAge, tui.HumanBytes(int64(r.Prune.KeepUnder))),
		)
	}
	if len(p.Plan.Actions) == 0 {
		for _, sk := range p.Plan.Skipped {
			lines = append(lines, fmt.Sprintf("Nothing to prune: %s.", sk.Reason))
		}
		return lines
	}
	lines = append(lines, "DRY RUN — the command that WOULD run (nothing executed):")
	for _, a := range p.Plan.Actions {
		lines = append(lines, "  "+tui.ActionLine(a))
	}
	lines = append(lines,
		fmt.Sprintf("Would delete about %s in %d entries not used since %s, leaving about %s.",
			tui.HumanBytes(s.Bytes), s.Files, s.Cutoff.Local().Format("2006-01-02 15:04"), tui.HumanBytes(s.CacheBytes-s.Bytes)),
		"Direct delete: no Trash, no undo. The next go build recompiles what it needs.",
	)
	if len(p.Running) > 0 {
		lines = append(lines, fmt.Sprintf("A build is running (%s): --yes refuses until it has finished.", strings.Join(p.Running, ", ")))
	}
	return append(lines, fmt.Sprintf("Run `regrow prune %s --yes` to execute.", autopilot.Name(r)))
}
