package engine

import (
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/trash"
)

// ActionKind says how an action deletes: a steward command, or a move
// to the Trash (ARCHITECTURE.md invariant 4: native commands first).
type ActionKind string

const (
	ActionNative ActionKind = "native"
	ActionTrash  ActionKind = "trash"
)

// Action is one exact command the plan would run. Nothing here
// executes in Phase 1: the planner's output is the contract the
// executor (Phase 2) fulfils.
type Action struct {
	RuleID string     `json:"rule_id"`
	Kind   ActionKind `json:"kind"`
	// Command is the exact argv, sudo included when the rule needs it.
	Command []string `json:"command"`
	// Path is the filesystem target for trash actions.
	Path  string `json:"path,omitempty"`
	Bytes int64  `json:"bytes"`
}

// Skip records why a selected finding produced no action.
type Skip struct {
	RuleID string `json:"rule_id"`
	Reason string `json:"reason"`
}

// Plan is the dry-run output: the exact command list, plus what was
// deliberately not planned and why.
type Plan struct {
	Actions []Action `json:"actions"`
	Skipped []Skip   `json:"skipped,omitempty"`
}

// TotalBytes sums the bytes the plan would reclaim.
func (p Plan) TotalBytes() int64 {
	var n int64
	for _, a := range p.Actions {
		n += a.Bytes
	}
	return n
}

// BuildPlan turns selected findings into the exact command list.
// Selection is by rule id; nil selects every finding. Architectural
// invariants are enforced here, not in the UI: surface-only rules
// never produce actions, and every trash target passes the path guard.
func BuildPlan(host Host, findings []Finding, selected map[string]bool) Plan {
	var plan Plan
	for _, f := range findings {
		if selected != nil && !selected[f.Rule.ID] {
			continue
		}
		if f.Rule.Risk == RiskSurfaceOnly {
			plan.Skipped = append(plan.Skipped, Skip{f.Rule.ID, "surface-only: report, never delete"})
			continue
		}
		if len(f.Items) == 0 {
			continue
		}
		if len(f.Rule.NativeCommand) > 0 {
			plan.Actions = append(plan.Actions, nativeActions(f)...)
			continue
		}
		for _, it := range f.Items {
			if it.Path == "" {
				plan.Skipped = append(plan.Skipped, Skip{f.Rule.ID, fmt.Sprintf("item %q has no path and the rule has no native command", it.Label)})
				continue
			}
			if err := trash.GuardPath(it.Path, host.Home); err != nil {
				plan.Skipped = append(plan.Skipped, Skip{f.Rule.ID, err.Error()})
				continue
			}
			plan.Actions = append(plan.Actions, Action{
				RuleID:  f.Rule.ID,
				Kind:    ActionTrash,
				Command: trashCommand(it.Path),
				Path:    it.Path,
				Bytes:   it.Bytes,
			})
		}
	}
	return plan
}

// nativeActions expands the rule's native command. With a {path} or
// {arg} placeholder the command runs once per item; otherwise once
// for the whole rule.
func nativeActions(f Finding) []Action {
	argv := []string(f.Rule.NativeCommand)
	perItem := false
	for _, tok := range argv {
		if strings.Contains(tok, "{path}") || strings.Contains(tok, "{arg}") {
			perItem = true
			break
		}
	}
	if !perItem {
		return []Action{{
			RuleID:  f.Rule.ID,
			Kind:    ActionNative,
			Command: withSudo(f.Rule.Sudo, argv),
			Bytes:   f.TotalBytes(),
		}}
	}
	actions := make([]Action, 0, len(f.Items))
	for _, it := range f.Items {
		cmd := make([]string, len(argv))
		for i, tok := range argv {
			tok = strings.ReplaceAll(tok, "{path}", it.Path)
			tok = strings.ReplaceAll(tok, "{arg}", it.Arg)
			cmd[i] = tok
		}
		actions = append(actions, Action{
			RuleID:  f.Rule.ID,
			Kind:    ActionNative,
			Command: withSudo(f.Rule.Sudo, cmd),
			Path:    it.Path,
			Bytes:   it.Bytes,
		})
	}
	return actions
}

func withSudo(sudo bool, argv []string) []string {
	if !sudo {
		return argv
	}
	return append([]string{"sudo"}, argv...)
}

// trashCommand is the exact command the executor would run today:
// Finder handles the move so the OS "Put Back" works. Phase 2 wraps
// this with a staging-dir fallback and the oplog.
func trashCommand(path string) []string {
	script := fmt.Sprintf("tell application %q to delete POSIX file %q", "Finder", path)
	return []string{"osascript", "-e", script}
}
