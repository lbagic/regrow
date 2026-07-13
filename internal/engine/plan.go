package engine

import (
	"fmt"

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
		if !f.Rule.Risk.Actionable() {
			plan.Skipped = append(plan.Skipped, Skip{f.Rule.ID, "surface-only: report, never delete"})
			continue
		}
		if len(f.Items) == 0 {
			continue
		}
		if len(f.Rule.NativeCommand) > 0 {
			actions, skips := nativeActions(f)
			plan.Actions = append(plan.Actions, actions...)
			plan.Skipped = append(plan.Skipped, skips...)
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
				Command: trash.PreviewCommand(it.Path),
				Path:    it.Path,
				Bytes:   it.Bytes,
			})
		}
	}
	return plan
}

// nativeActions expands the rule's native command. With a placeholder
// the command runs once per item; otherwise once for the whole rule.
// The placeholder convention itself (which tokens exist, refusing
// empty substitutions) is owned by the schema (Argv).
func nativeActions(f Finding) ([]Action, []Skip) {
	if !f.Rule.NativeCommand.PerItem() {
		return []Action{{
			RuleID:  f.Rule.ID,
			Kind:    ActionNative,
			Command: withSudo(f.Rule.Sudo, f.Rule.NativeCommand),
			Bytes:   f.TotalBytes(),
		}}, nil
	}
	var actions []Action
	var skips []Skip
	for _, it := range f.Items {
		cmd, err := f.Rule.NativeCommand.ExpandItem(it)
		if err != nil {
			skips = append(skips, Skip{f.Rule.ID, err.Error()})
			continue
		}
		actions = append(actions, Action{
			RuleID:  f.Rule.ID,
			Kind:    ActionNative,
			Command: withSudo(f.Rule.Sudo, cmd),
			Path:    it.Path,
			Bytes:   it.Bytes,
		})
	}
	return actions, skips
}

func withSudo(sudo bool, argv []string) []string {
	if !sudo {
		return argv
	}
	return append([]string{"sudo"}, argv...)
}
