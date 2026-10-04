package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/autopilot"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/tui"
)

// runTick is one pass of the watch loop from the terminal. A tick that
// sampled is printed even when it also failed (a refused autotrim, a
// history that could not be written), and the error still exits 1.
func runTick(host engine.Host, catalog []engine.Rule, opts options) error {
	ap, err := autopilot.New(host, catalog)
	if err != nil {
		return err
	}
	tick, err := ap.Tick(context.Background(), opts.autotrim)
	if tick.At.IsZero() {
		return err
	}
	if opts.asJSON {
		if jerr := emitJSON(tick); jerr != nil {
			return jerr
		}
		return err
	}
	for _, line := range tickLines(tick) {
		fmt.Println(line)
	}
	return err
}

func tickLines(t headroom.Tick) []string {
	lines := []string{fmt.Sprintf("%s  free %s of %s, purgeable %s, swap %s",
		t.At.Local().Format("2006-01-02 15:04"), tui.HumanBytes(t.Free), tui.HumanBytes(t.Total),
		tui.HumanBytes(t.Purgeable), tui.HumanBytes(t.SwapUsed))}
	if t.DaysToFull != nil {
		lines = append(lines, fmt.Sprintf("Days to full: %.1f at the current rate.", *t.DaysToFull))
	} else {
		lines = append(lines, "Days to full: no forecast (it needs three hours of samples with free space falling).")
	}
	for _, a := range t.Alerts {
		lines = append(lines, "ALERT  "+a.Message)
	}
	if t.Pruned != nil {
		lines = append(lines, pruneResultLines(*t.Pruned)...)
	}
	return lines
}

// pruneResultLines keeps the bytes deleted apart from the statfs
// readings: Time Machine snapshots can hold freed blocks, so the two
// need not agree.
func pruneResultLines(res headroom.PruneResult) []string {
	if res.Skipped != "" {
		return []string{fmt.Sprintf("Prune %s: not run: %s.", res.RuleID, strings.TrimSuffix(res.Skipped, "."))}
	}
	lines := []string{
		fmt.Sprintf("Prune %s: deleted %s in %d entries. Run %s.", res.RuleID, tui.HumanBytes(res.Bytes), res.Files, res.Run),
		fmt.Sprintf("  Free space  %s before, %s after (snapshots can hold freed blocks for a while)",
			tui.HumanBytes(res.FreeBefore), tui.HumanBytes(res.FreeAfter)),
	}
	if res.Error != "" {
		lines = append(lines, "  failed: "+res.Error)
	}
	return lines
}
