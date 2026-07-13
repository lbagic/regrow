// Command regrow scans the disk for regenerable caches and junk,
// explains what everything is and how it comes back, and reclaims
// space reversibly (dry-run → trash → undo).
//
//	regrow [scan] [--rules-dir DIR] [--json]   interactive checklist on a TTY;
//	                                           plain listing when piped or --json
//	regrow plan [id ...] [--json]              dry-run: exact command list
//	regrow clean [id ...] [--yes]              execute: no ids = safe rules only
//
// Ids are rule ids ("sim-devices") or item ids ("sim-devices/AAA-111",
// as listed by scan output and the TUI footer).
//
//	regrow undo [run-id]                       restore the last (or given) run
//	regrow history [--json]                    past runs from the oplog
//	regrow rules                               list the catalog
//	regrow version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/tui"
)

// version is overridden at release time via -ldflags.
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "regrow:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "scan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	rulesDir := fs.String("rules-dir", "", "load rules from a directory instead of the embedded catalog")
	asJSON := fs.Bool("json", false, "machine-readable output")
	betaRules := fs.Bool("beta-rules", false, "include rules still in staged rollout")
	yes := fs.Bool("yes", false, "clean: skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if cmd == "version" {
		fmt.Println("regrow", version)
		return nil
	}

	catalog, err := loadCatalog(*rulesDir)
	if err != nil {
		return err
	}
	if !*betaRules {
		catalog = engine.WithoutBeta(catalog)
	}
	host := engine.DetectHost()

	switch cmd {
	case "rules":
		return printRules(catalog, *asJSON)
	case "scan":
		if !*asJSON && isTTY() {
			return tui.Run(host, version, func(ctx context.Context) []engine.Finding {
				return scanner.New(host).Scan(ctx, catalog)
			})
		}
		findings := scanner.New(host).Scan(context.Background(), catalog)
		return printFindings(findings, *asJSON)
	case "plan":
		findings := scanner.New(host).Scan(context.Background(), catalog)
		plan := engine.BuildPlan(host, findings, selection(fs.Args()))
		return printPlan(plan, *asJSON)
	case "clean":
		return runClean(host, catalog, fs.Args(), *yes)
	case "undo":
		return runUndo(fs.Args())
	case "history":
		return runHistory(*asJSON)
	default:
		return fmt.Errorf("unknown command %q (scan, plan, clean, undo, history, rules, version)", cmd)
	}
}

func loadCatalog(dir string) ([]engine.Rule, error) {
	if dir != "" {
		return engine.LoadDir(dir)
	}
	return engine.LoadEmbedded()
}

// selection turns `regrow plan id1 id2` args into a selection set;
// no args means plan everything the scan found.
func selection(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	sel := make(map[string]bool, len(ids))
	for _, id := range ids {
		sel[id] = true
	}
	return sel
}

func printRules(catalog []engine.Rule, asJSON bool) error {
	if asJSON {
		return emitJSON(catalog)
	}
	for _, r := range catalog {
		fmt.Printf("%-24s %-12s %-10s %s\n", r.ID, r.Category, r.Risk, r.Title)
	}
	return nil
}

func printFindings(findings []engine.Finding, asJSON bool) error {
	if asJSON {
		return emitJSON(findings)
	}
	// Group by category, largest first inside each (PRODUCT.md §4).
	byCategory := map[string][]engine.Finding{}
	for _, f := range findings {
		byCategory[f.Rule.Category] = append(byCategory[f.Rule.Category], f)
	}
	categories := make([]string, 0, len(byCategory))
	for c := range byCategory {
		categories = append(categories, c)
	}
	sort.Slice(categories, func(i, j int) bool {
		return categoryBytes(byCategory[categories[i]]) > categoryBytes(byCategory[categories[j]])
	})

	var total int64
	for _, c := range categories {
		group := byCategory[c]
		sort.Slice(group, func(i, j int) bool { return group[i].TotalBytes() > group[j].TotalBytes() })
		fmt.Printf("%s  %s\n", strings.ToUpper(c), tui.HumanBytes(categoryBytes(group)))
		for _, f := range group {
			total += f.TotalBytes()
			switch {
			case f.Err != "":
				fmt.Printf("  ! %-32s %10s  %s (%s)\n", f.Rule.Title, tui.HumanBytes(f.TotalBytes()), f.Rule.Risk, f.Err)
			case len(f.Items) == 0:
				fmt.Printf("  - %-32s %10s  not found\n", f.Rule.Title, "")
			default:
				fmt.Printf("  • %-32s %10s  %-12s %s\n", f.Rule.Title, tui.HumanBytes(f.TotalBytes()), f.Rule.Risk, f.Rule.Regen.Story)
				for _, it := range f.Items {
					fmt.Printf("      %10s  %s\n", tui.HumanBytes(it.Bytes), engine.ItemID(f.Rule.ID, it.Key))
				}
			}
		}
	}
	fmt.Printf("\nTotal found: %s. Dry-run: `regrow plan` shows the exact commands; nothing was deleted.\n", tui.HumanBytes(total))
	return nil
}

func printPlan(plan engine.Plan, asJSON bool) error {
	if asJSON {
		return emitJSON(plan)
	}
	if len(plan.Actions) == 0 && len(plan.Skipped) == 0 && len(plan.Unmatched) == 0 {
		fmt.Println("Nothing to plan: no selected rule found anything.")
		return nil
	}
	fmt.Println("DRY RUN — commands that WOULD run (nothing executed):")
	for _, a := range plan.Actions {
		fmt.Printf("  [%s] %-24s %10s  %s\n", a.Kind, a.RuleID, tui.HumanBytes(a.Bytes), tui.ShellJoin(a.Command))
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  [skip] %-22s %s\n", s.RuleID, s.Reason)
	}
	for _, u := range plan.Unmatched {
		fmt.Printf("  [unmatched] %-17s selector matched nothing in this scan\n", u)
	}
	fmt.Printf("\nWould reclaim: %s\n", tui.HumanBytes(plan.TotalBytes()))
	return nil
}

func categoryBytes(group []engine.Finding) int64 {
	var n int64
	for _, f := range group {
		n += f.TotalBytes()
	}
	return n
}

// isTTY: the interactive UI needs a terminal on both ends.
func isTTY() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
