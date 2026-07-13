package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/trash"
	"github.com/lbagic/regrow/internal/tui"
)

// runClean is the execution opt-in (invariant 1: dry-run is the
// default; this command IS the opt-in). Without ids only safe rules
// run — the same set the TUI pre-selects; caution rules must be named
// explicitly. The plan is shown and confirmed before anything moves.
func runClean(host engine.Host, catalog []engine.Rule, ids []string, yes bool) error {
	findings := scanner.New(host).Scan(context.Background(), catalog)
	sel := selection(ids)
	if sel == nil {
		sel = safeSelection(findings)
	}
	plan := engine.BuildPlan(host, findings, sel)
	if len(plan.Actions) == 0 {
		fmt.Println("Nothing to clean: no selected rule found anything.")
		return nil
	}

	fmt.Println("About to execute:")
	for _, a := range plan.Actions {
		fmt.Printf("  [%s] %-24s %10s  %s\n", a.Kind, a.RuleID, tui.HumanBytes(a.Bytes), tui.ShellJoin(a.Command))
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  [skip] %-22s %s\n", s.RuleID, s.Reason)
	}
	fmt.Printf("Total: %s → Trash (undo: `regrow undo`)\n", tui.HumanBytes(plan.TotalBytes()))
	if !yes {
		if !isTTY() {
			return fmt.Errorf("refusing to execute without a terminal; pass --yes to confirm")
		}
		fmt.Print("Proceed? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Println("Aborted; nothing was executed.")
			return nil
		}
	}

	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	log, err := oplog.Open(logPath)
	if err != nil {
		return err
	}
	defer log.Close()

	runID := executor.NewRunID(time.Now())
	stateDir := filepath.Dir(logPath)
	exec := &executor.Executor{
		Trash: &trash.Mover{Home: host.Home, StagingDir: filepath.Join(stateDir, "staging", runID)},
		Log:   log,
		RunID: runID,
	}
	res, err := exec.Execute(context.Background(), plan)
	if err != nil {
		return err
	}

	fmt.Printf("\nDone: %d ok, %d failed, %s reclaimed. Run %s — `regrow undo` restores trash moves.\n",
		res.Done, res.Failed, tui.HumanBytes(res.Bytes), res.RunID)
	for _, f := range res.Failures {
		fmt.Println("  failed:", f)
	}
	return nil
}

// safeSelection mirrors the TUI's pre-selection: safe rules with items.
func safeSelection(findings []engine.Finding) map[string]bool {
	sel := map[string]bool{}
	for _, f := range findings {
		if f.Rule.Risk == engine.RiskSafe && len(f.Items) > 0 {
			sel[f.Rule.ID] = true
		}
	}
	return sel
}

// runUndo restores the newest run that still has something to restore,
// or the given run id.
func runUndo(args []string) error {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	entries, err := oplog.Read(logPath)
	if err != nil {
		return err
	}
	runs := oplog.Runs(entries)

	var target *oplog.Run
	if len(args) > 0 {
		for i := range runs {
			if runs[i].ID == args[0] {
				target = &runs[i]
			}
		}
		if target == nil {
			return fmt.Errorf("run %q not in the oplog (see `regrow history`)", args[0])
		}
	} else {
		for i := len(runs) - 1; i >= 0; i-- {
			if len(runs[i].Undoable()) > 0 {
				target = &runs[i]
				break
			}
		}
		if target == nil {
			fmt.Println("Nothing to undo: no run has restorable trash moves.")
			return nil
		}
	}

	log, err := oplog.Open(logPath)
	if err != nil {
		return err
	}
	defer log.Close()

	exec := &executor.Executor{Log: log}
	res, err := exec.Undo(*target)
	if err != nil {
		return err
	}
	fmt.Printf("Undo of run %s: %d restored, %d failed.\n", target.ID, res.Restored, res.Failed)
	for _, f := range res.Failures {
		fmt.Println("  failed:", f)
	}
	if res.NativeSkipped > 0 {
		fmt.Printf("  %d native command(s) are not undoable — their data comes back via the regen story (`regrow rules`).\n", res.NativeSkipped)
	}
	return nil
}

func runHistory(asJSON bool) error {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	entries, err := oplog.Read(logPath)
	if err != nil {
		return err
	}
	runs := oplog.Runs(entries)
	if asJSON {
		return emitJSON(runs)
	}
	if len(runs) == 0 {
		fmt.Println("No history yet: nothing has been executed on this machine.")
		return nil
	}
	for _, r := range runs {
		var done, failed, undone int
		var bytes int64
		for _, e := range r.Entries {
			switch e.Event {
			case oplog.EventDone:
				done++
			case oplog.EventFail:
				failed++
			case oplog.EventUndo:
				if e.Error == "" {
					undone++
				}
			}
			if e.Event == oplog.EventStart {
				bytes += e.Bytes
			}
		}
		status := fmt.Sprintf("%d ok, %d failed", done, failed)
		if undone > 0 {
			status += fmt.Sprintf(", %d undone", undone)
		}
		if rest := len(r.Undoable()); rest > 0 {
			status += fmt.Sprintf(" (%d restorable)", rest)
		}
		fmt.Printf("%s  %s  %10s  %s\n", r.ID, r.Start.Local().Format("2006-01-02 15:04"), tui.HumanBytes(bytes), status)
	}
	return nil
}
