package main

import (
	"context"
	"fmt"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/tui"
)

// runDoctor is the hero-bug scan (PRODUCT.md §3): a fast pass over
// only the rules that carry a doctor block, plus the phantom-space
// explainers. Read-only by construction — doctor never plans and
// never executes; the fix lines point at `regrow clean`.
func runDoctor(host engine.Host, catalog []engine.Rule, asJSON bool) error {
	rules := engine.DoctorRules(catalog)
	findings := scanner.New(host).Scan(context.Background(), rules)
	report := engine.BuildDoctorReport(findings)
	if asJSON {
		return emitJSON(report)
	}
	printDoctorReport(report)
	return nil
}

func printDoctorReport(rep engine.DoctorReport) {
	fmt.Println("regrow doctor — known runaway bugs, and where the \"missing\" space hides")
	fmt.Println()

	fmt.Println("HERO BUGS")
	flagged := 0
	for _, c := range rep.Hero {
		f := c.Finding
		switch {
		case f.Err != "":
			fmt.Printf("  ! %-34s %10s  scan failed: %s\n", f.Rule.Title, "", f.Err)
		case c.Flagged:
			flagged++
			fmt.Printf("  🚩 %-33s %10s  RUNAWAY — healthy is under %s\n",
				f.Rule.Title, tui.HumanBytes(f.TotalBytes()), tui.HumanBytes(int64(f.Rule.Doctor.FlagAbove)))
			fmt.Printf("       %s\n", f.Rule.Doctor.Story)
			fmt.Printf("       fix: regrow clean %s%s\n", f.Rule.ID, nativeHint(f.Rule))
		case len(f.Items) == 0:
			fmt.Printf("  ✓ %-34s %10s  not present\n", f.Rule.Title, "")
		default:
			fmt.Printf("  ✓ %-34s %10s  normal (flags above %s)\n",
				f.Rule.Title, tui.HumanBytes(f.TotalBytes()), tui.HumanBytes(int64(f.Rule.Doctor.FlagAbove)))
		}
	}

	fmt.Println()
	fmt.Println("PHANTOM SPACE — why Finder shows more used than your files add up to")
	for _, f := range rep.Phantom {
		switch {
		case f.Err != "":
			fmt.Printf("  ! %-34s %10s  scan failed: %s\n", f.Rule.Title, "", f.Err)
		case len(f.Items) == 0:
			fmt.Printf("  • %-34s %10s  not present\n", f.Rule.Title, "")
		default:
			fmt.Printf("  • %-34s %10s\n", f.Rule.Title, tui.HumanBytes(f.TotalBytes()))
			for _, it := range f.Items {
				fmt.Printf("      %s\n", itemLine(it))
			}
			if f.Rule.Note != "" {
				fmt.Printf("      %s\n", f.Rule.Note)
			}
		}
	}

	fmt.Println()
	if flagged > 0 {
		fmt.Printf("%d runaway bug(s) found. Fixes above are dry-run first: `regrow plan <id>` shows the exact commands.\n", flagged)
	} else {
		fmt.Println("No runaway bugs on this machine. Phantom space above is informational — nothing needs fixing.")
	}
}

// itemLine renders a phantom item label; the size already prints on
// the rule's title line, and labels carry their own story
// ("Docker.raw — 35.2 GiB real of 64.0 GiB sparse").
func itemLine(it engine.Item) string {
	if it.Label != "" {
		return it.Label
	}
	return it.Path
}

// nativeHint spells out the steward command a fix runs, so the
// screenshot alone tells the reader what would execute.
func nativeHint(r engine.Rule) string {
	if len(r.NativeCommand) == 0 {
		return "  (moves to Trash, undoable)"
	}
	cmd := tui.ShellJoin(r.NativeCommand)
	if r.Sudo {
		cmd = "sudo " + cmd
	}
	return fmt.Sprintf("  (runs: %s)", cmd)
}
