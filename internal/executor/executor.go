// Package executor runs a plan: journal, act, journal again. It is a
// thin caller over two seams — the trash mover and the oplog — so the
// mechanisms stay testable and swappable. Execution is opt-in per run
// (invariant 1); the executor never decides what to run, only how.
package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// Mover is the trash seam the executor crosses for trash actions.
type Mover interface {
	Move(ctx context.Context, path string) (trash.Receipt, error)
}

// Journal is the oplog seam. Append must persist before returning.
type Journal interface {
	Append(oplog.Entry) error
}

// Executor runs plan actions one at a time, journaling each before and
// after. One failed action never aborts the run: every action is
// independent, and a half-finished run is exactly what the oplog and
// undo exist to make safe.
type Executor struct {
	Trash Mover
	Log   Journal
	// RunNative executes a native steward command. Nil means real
	// exec with inherited stdio (sudo can prompt, docker can stream).
	RunNative func(ctx context.Context, argv []string) error
	// Now is injectable for deterministic journal timestamps in tests.
	Now func() time.Time
}

// Result summarises one executed run.
type Result struct {
	RunID    string
	Done     int
	Failed   int
	Bytes    int64 // reclaimed by successful actions
	Failures []string
}

// NewRunID mints a journal run id: sortable timestamp + entropy so
// two runs in the same second never collide.
func NewRunID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Execute runs every action in the plan. The journal line for an
// action is written and synced before the action runs (invariant 6).
func (e *Executor) Execute(ctx context.Context, plan engine.Plan) (Result, error) {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	runNative := e.RunNative
	if runNative == nil {
		runNative = execNative
	}

	res := Result{RunID: NewRunID(now())}
	for seq, a := range plan.Actions {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		entry := oplog.Entry{
			Time: now(), Run: res.RunID, Seq: seq + 1, Event: oplog.EventStart,
			RuleID: a.RuleID, Kind: string(a.Kind), Command: a.Command, Path: a.Path, Bytes: a.Bytes,
		}
		if err := e.Log.Append(entry); err != nil {
			// Journal down = no action: the invariant is not optional.
			return res, fmt.Errorf("oplog append failed, refusing to act: %w", err)
		}

		var receipt *trash.Receipt
		var actErr error
		switch a.Kind {
		case engine.ActionTrash:
			r, err := e.Trash.Move(ctx, a.Path)
			if err == nil {
				receipt = &r
			}
			actErr = err
		case engine.ActionNative:
			actErr = runNative(ctx, a.Command)
		default:
			actErr = fmt.Errorf("unknown action kind %q", a.Kind)
		}

		after := oplog.Entry{
			Time: now(), Run: res.RunID, Seq: seq + 1, Event: oplog.EventDone,
			RuleID: a.RuleID, Receipt: receipt,
		}
		if actErr != nil {
			after.Event = oplog.EventFail
			after.Error = actErr.Error()
			res.Failed++
			res.Failures = append(res.Failures, fmt.Sprintf("%s: %v", a.RuleID, actErr))
		} else {
			res.Done++
			res.Bytes += a.Bytes
		}
		if err := e.Log.Append(after); err != nil {
			return res, fmt.Errorf("oplog append failed after acting — journal is incomplete: %w", err)
		}
	}
	return res, nil
}

// UndoResult summarises one undo pass.
type UndoResult struct {
	Restored int
	Failed   int
	Failures []string
	// NativeSkipped counts the run's native actions, which are not
	// undoable — their regen story is the recovery path.
	NativeSkipped int
}

// Undo restores a run's undoable trash receipts, last moved first,
// journaling every attempt under the same run id.
func (e *Executor) Undo(run oplog.Run) (UndoResult, error) {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	var res UndoResult
	for _, entry := range run.Entries {
		if entry.Event == oplog.EventDone && entry.Receipt == nil {
			res.NativeSkipped++
		}
	}
	for _, entry := range run.Undoable() {
		err := trash.Restore(*entry.Receipt)
		undoEntry := oplog.Entry{
			Time: now(), Run: run.ID, Seq: entry.Seq, Event: oplog.EventUndo,
			RuleID: entry.RuleID, Path: entry.Receipt.Original,
		}
		if err != nil {
			undoEntry.Error = err.Error()
			res.Failed++
			res.Failures = append(res.Failures, err.Error())
		} else {
			res.Restored++
		}
		if logErr := e.Log.Append(undoEntry); logErr != nil {
			return res, fmt.Errorf("oplog append failed during undo: %w", logErr)
		}
	}
	return res, nil
}

// execNative runs a steward command with inherited stdio: sudo can
// prompt, docker can stream progress, the user sees what runs.
func execNative(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
