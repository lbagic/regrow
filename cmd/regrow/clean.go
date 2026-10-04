package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/docker"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/trash"
	"github.com/lbagic/regrow/internal/tui"
)

// runClean is the execution opt-in (invariant 1: dry-run is the
// default; this command IS the opt-in). Without ids the default
// selection runs, the set the TUI pre-ticks; caution rules must be
// named explicitly. The plan is shown and confirmed before anything
// moves.
func runClean(host engine.Host, catalog []engine.Rule, ids []string, yes bool) error {
	findings := scanner.New(host).Scan(context.Background(), catalog)
	plan := engine.BuildPlan(host, findings, selectionFor(ids, findings))
	if len(plan.Unmatched) > 0 {
		// A typo'd selector must never quietly execute less (or, for a
		// rule atom, more) than the user meant.
		hint := "`regrow scan` lists rules and item ids"
		for _, u := range plan.Unmatched {
			if strings.HasPrefix(u, "-") {
				hint = "flags go before ids: `regrow clean --yes id ...`"
				break
			}
		}
		return fmt.Errorf("selector(s) matched nothing in this scan: %s\n(%s)",
			strings.Join(plan.Unmatched, ", "), hint)
	}
	if len(plan.Actions) == 0 {
		fmt.Println("Nothing to clean: no selected rule found anything.")
		return nil
	}

	printPlanActions(plan)
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
	return executePlan(host, plan)
}

// printPlanActions prints the about-to-run action list — before the
// clean prompt, and after a TUI-confirmed run so the terminal
// scrollback keeps a record of what executed once the alt-screen is
// gone.
func printPlanActions(plan engine.Plan) {
	fmt.Println("About to execute:")
	for _, a := range plan.Actions {
		fmt.Println("  " + tui.ActionLine(a))
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  [skip] %-22s %s\n", s.RuleID, s.Reason)
	}
	for _, line := range tui.TotalsLines(plan) {
		fmt.Println(line)
	}
	fmt.Printf("Total: %s\n", tui.HumanBytes(plan.TotalBytes()))
}

// executePlan runs an already-confirmed plan: oplog first, then the
// executor. Both confirmation paths (clean prompt, TUI plan → x → y)
// land here.
func executePlan(host engine.Host, plan engine.Plan) error {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	log, err := oplog.Open(logPath)
	if err != nil {
		return err
	}
	// Journal entries fsync per Append (invariant 6); nothing left to
	// lose at close.
	defer func() { _ = log.Close() }()

	// The volume export cap comes from user config; a broken config
	// must block execution, not silently fall back to defaults.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	capBytes, err := cfg.Docker.ExportCapBytes()
	if err != nil {
		return err
	}

	runID := executor.NewRunID(time.Now())
	stateDir := filepath.Dir(logPath)
	stagingDir := filepath.Join(stateDir, "staging", runID)
	exec := &executor.Executor{
		Trash: &trash.Mover{Home: host.Home, StagingDir: stagingDir},
		Log:   log,
		PreActions: map[string]executor.PreAction{
			engine.PreActionVolumeExport: (&docker.Exporter{StagingDir: stagingDir, CapBytes: capBytes}).PreAction,
		},
		RunID: runID,
	}
	res, err := exec.Execute(context.Background(), plan)
	if err != nil {
		return err
	}

	fmt.Printf("\nDone: %d ok, %d failed. Freed now: %s. In the Trash: %s, freed once it is emptied. Run %s — `regrow undo` restores Trash moves.\n",
		res.Done, res.Failed, tui.HumanBytes(res.Bytes-res.TrashBytes), tui.HumanBytes(res.TrashBytes), res.RunID)
	for _, f := range res.Failures {
		fmt.Println("  failed:", f)
	}
	return nil
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
	// Journal entries fsync per Append (invariant 6); nothing left to
	// lose at close.
	defer func() { _ = log.Close() }()

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
	if res.ExportSkipped > 0 {
		fmt.Printf("  %d docker volume(s) are not auto-restorable — the tarball in regrow staging is the recovery copy (`regrow history` shows where).\n", res.ExportSkipped)
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
