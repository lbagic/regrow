// Command regrow scans the disk for regenerable caches and junk,
// explains what everything is and how it comes back, and reclaims
// space reversibly (dry-run → trash → undo).
//
// Phase 1C surface (the TUI lands in Phase 1D):
//
//	regrow [scan] [--rules-dir DIR] [--json]   measure every rule
//	regrow plan [id ...] [--json]              dry-run: exact command list
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
	host := engine.DetectHost()

	switch cmd {
	case "rules":
		return printRules(catalog, *asJSON)
	case "scan":
		findings := scanner.New(host).Scan(context.Background(), catalog)
		return printFindings(findings, *asJSON)
	case "plan":
		findings := scanner.New(host).Scan(context.Background(), catalog)
		plan := engine.BuildPlan(host, findings, selection(fs.Args()))
		return printPlan(plan, *asJSON)
	default:
		return fmt.Errorf("unknown command %q (scan, plan, rules, version)", cmd)
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
		fmt.Printf("%s  %s\n", strings.ToUpper(c), humanBytes(categoryBytes(group)))
		for _, f := range group {
			total += f.TotalBytes()
			switch {
			case f.Err != "":
				fmt.Printf("  ! %-32s %10s  %s (%s)\n", f.Rule.Title, humanBytes(f.TotalBytes()), f.Rule.Risk, f.Err)
			case len(f.Items) == 0:
				fmt.Printf("  - %-32s %10s  not found\n", f.Rule.Title, "")
			default:
				fmt.Printf("  • %-32s %10s  %-12s %s\n", f.Rule.Title, humanBytes(f.TotalBytes()), f.Rule.Risk, f.Rule.Regen.Story)
			}
		}
	}
	fmt.Printf("\nTotal found: %s. Dry-run: `regrow plan` shows the exact commands; nothing was deleted.\n", humanBytes(total))
	return nil
}

func printPlan(plan engine.Plan, asJSON bool) error {
	if asJSON {
		return emitJSON(plan)
	}
	if len(plan.Actions) == 0 && len(plan.Skipped) == 0 {
		fmt.Println("Nothing to plan: no selected rule found anything.")
		return nil
	}
	fmt.Println("DRY RUN — commands that WOULD run (nothing executed):")
	for _, a := range plan.Actions {
		fmt.Printf("  [%s] %-24s %10s  %s\n", a.Kind, a.RuleID, humanBytes(a.Bytes), shellJoin(a.Command))
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  [skip] %-22s %s\n", s.RuleID, s.Reason)
	}
	fmt.Printf("\nWould reclaim: %s\n", humanBytes(plan.TotalBytes()))
	return nil
}

func categoryBytes(group []engine.Finding) int64 {
	var n int64
	for _, f := range group {
		n += f.TotalBytes()
	}
	return n
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// shellJoin renders argv for display, quoting args with spaces.
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"'") {
			parts[i] = fmt.Sprintf("%q", a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
