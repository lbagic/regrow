package engine

import (
	"fmt"
	"sort"

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
	RuleID string `json:"rule_id"`
	// ItemKey identifies the item this action targets; empty for
	// whole-rule commands, which act on every item at once.
	ItemKey string     `json:"item_key,omitempty"`
	Kind    ActionKind `json:"kind"`
	// Command is the exact argv, sudo included when the rule needs it.
	Command []string `json:"command"`
	// Path is the filesystem target for trash actions.
	Path  string `json:"path,omitempty"`
	Bytes int64  `json:"bytes"`
}

// Skip records why a selected finding produced no action.
type Skip struct {
	RuleID  string `json:"rule_id"`
	ItemKey string `json:"item_key,omitempty"`
	Reason  string `json:"reason"`
}

// Plan is the dry-run output: the exact command list, plus what was
// deliberately not planned and why.
type Plan struct {
	Actions []Action `json:"actions"`
	Skipped []Skip   `json:"skipped,omitempty"`
	// Unmatched lists selection atoms that addressed nothing in these
	// findings — an unknown rule id, or an item key absent from the
	// scan. A typo'd selector must be visible, never a silent no-op:
	// `clean` refuses to run on it, `plan` warns.
	Unmatched []string `json:"unmatched,omitempty"`
}

// TotalBytes sums the bytes the plan would reclaim.
func (p Plan) TotalBytes() int64 {
	var n int64
	for _, a := range p.Actions {
		n += a.Bytes
	}
	return n
}

// selection is the parsed form of the atom map: whole-rule atoms and
// per-item atoms ("ruleID/key"), kept separate so partial selections
// are detectable.
type selection struct {
	all   bool
	rules map[string]bool
	items map[string]map[string]bool
}

func parseSelection(selected map[string]bool) selection {
	if selected == nil {
		return selection{all: true}
	}
	sel := selection{rules: map[string]bool{}, items: map[string]map[string]bool{}}
	for atom, on := range selected {
		if !on {
			continue
		}
		ruleID, key, isItem := SplitItemID(atom)
		if !isItem {
			sel.rules[ruleID] = true
			continue
		}
		if sel.items[ruleID] == nil {
			sel.items[ruleID] = map[string]bool{}
		}
		sel.items[ruleID][key] = true
	}
	return sel
}

// itemsFor resolves the selection against one finding: the items to
// plan, whether the rule is engaged at all, and whether the pick is a
// strict subset (partial). Matched item atoms are marked in `matched`
// so unaddressed atoms can be reported.
func (s selection) itemsFor(f Finding, home string, matched map[string]bool) (items []Item, engaged, partial bool) {
	if s.all || s.rules[f.Rule.ID] {
		return f.Items, true, false
	}
	keys := s.items[f.Rule.ID]
	if len(keys) == 0 {
		return nil, false, false
	}
	for _, it := range f.Items {
		key := it.Key
		if key == "" {
			key = it.DeriveKey(home)
		}
		if key != "" && keys[key] {
			items = append(items, it)
			matched[ItemID(f.Rule.ID, key)] = true
		}
	}
	return items, true, len(items) < len(f.Items)
}

// unmatched returns every selection atom that addressed nothing:
// rule atoms whose rule is not among the findings, and item atoms not
// marked in `matched`. Rule atoms whose rule found zero items are NOT
// unmatched — an absent target is a normal scan outcome, not a typo.
func (s selection) unmatched(findings []Finding, matched map[string]bool) []string {
	known := make(map[string]bool, len(findings))
	for _, f := range findings {
		known[f.Rule.ID] = true
	}
	var out []string
	for ruleID := range s.rules {
		if !known[ruleID] {
			out = append(out, ruleID)
		}
	}
	for ruleID, keys := range s.items {
		for key := range keys {
			if !matched[ItemID(ruleID, key)] {
				out = append(out, ItemID(ruleID, key))
			}
		}
	}
	sort.Strings(out)
	return out
}

// BuildPlan turns selected findings into the exact command list.
// Selection atoms are rule ids ("sim-devices") or item ids
// ("sim-devices/AAA-111"); nil selects every finding. Architectural
// invariants are enforced here, not in the UI: surface-only rules
// never produce actions, every trash target passes the path guard,
// and a whole-rule command is never planned for a partial selection —
// it would delete more than was selected.
func BuildPlan(host Host, findings []Finding, selected map[string]bool) Plan {
	var plan Plan
	sel := parseSelection(selected)
	matched := map[string]bool{}
	for _, f := range findings {
		items, engaged, partial := sel.itemsFor(f, host.Home, matched)
		if !engaged {
			continue
		}
		if !f.Rule.Risk.Actionable() {
			plan.Skipped = append(plan.Skipped, Skip{RuleID: f.Rule.ID, Reason: "surface-only: report, never delete"})
			continue
		}
		if len(items) == 0 {
			continue
		}
		// Resolve keys on a copy: scanner-produced findings carry them,
		// hand-built ones (tests, library callers) may not, and actions
		// must always name their item.
		resolved := make([]Item, len(items))
		copy(resolved, items)
		for i := range resolved {
			if resolved[i].Key == "" {
				resolved[i].Key = resolved[i].DeriveKey(host.Home)
			}
		}
		items = resolved
		if len(f.Rule.NativeCommand) > 0 {
			actions, skips := nativeActions(f.Rule, items, partial)
			plan.Actions = append(plan.Actions, actions...)
			plan.Skipped = append(plan.Skipped, skips...)
			continue
		}
		for _, it := range items {
			if it.Path == "" {
				plan.Skipped = append(plan.Skipped, Skip{RuleID: f.Rule.ID, ItemKey: it.Key, Reason: fmt.Sprintf("item %q has no path and the rule has no native command", it.Label)})
				continue
			}
			if err := trash.GuardPath(it.Path, host.Home); err != nil {
				plan.Skipped = append(plan.Skipped, Skip{RuleID: f.Rule.ID, ItemKey: it.Key, Reason: err.Error()})
				continue
			}
			plan.Actions = append(plan.Actions, Action{
				RuleID:  f.Rule.ID,
				ItemKey: it.Key,
				Kind:    ActionTrash,
				Command: trash.PreviewCommand(it.Path),
				Path:    it.Path,
				Bytes:   it.Bytes,
			})
		}
	}
	if !sel.all {
		plan.Unmatched = sel.unmatched(findings, matched)
	}
	return plan
}

// nativeActions expands the rule's native command over the selected
// items. With a placeholder the command runs once per item; without
// one it acts on everything at once, so a partial selection is
// refused (skipped with reason) instead of over-deleting. The
// placeholder convention itself (which tokens exist, refusing empty
// substitutions) is owned by the schema (Argv).
func nativeActions(r Rule, items []Item, partial bool) ([]Action, []Skip) {
	if !r.NativeCommand.PerItem() {
		if partial {
			return nil, []Skip{{RuleID: r.ID, Reason: "whole-rule command cannot target individual items — select the whole rule"}}
		}
		var total int64
		for _, it := range items {
			total += it.Bytes
		}
		return []Action{{
			RuleID:  r.ID,
			Kind:    ActionNative,
			Command: withSudo(r.Sudo, r.NativeCommand),
			Bytes:   total,
		}}, nil
	}
	var actions []Action
	var skips []Skip
	for _, it := range items {
		cmd, err := r.NativeCommand.ExpandItem(it)
		if err != nil {
			skips = append(skips, Skip{RuleID: r.ID, ItemKey: it.Key, Reason: err.Error()})
			continue
		}
		actions = append(actions, Action{
			RuleID:  r.ID,
			ItemKey: it.Key,
			Kind:    ActionNative,
			Command: withSudo(r.Sudo, cmd),
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
