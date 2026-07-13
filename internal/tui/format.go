package tui

import (
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// HumanBytes renders a byte count the way the sketch does: "20.1 GiB".
// The implementation lives in engine (scanner labels use it too);
// this alias keeps the TUI/CLI call sites stable.
func HumanBytes(n int64) string { return engine.HumanBytes(n) }

// ShellJoin renders argv for display, quoting args with spaces.
func ShellJoin(argv []string) string {
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

// ActionCommand renders an action's command for plan output, spelling
// out the pre-action so the export-then-remove sequence is visible on
// every plan surface, not just in the rule's note.
func ActionCommand(a engine.Action) string {
	s := ShellJoin(a.Command)
	switch a.PreAction {
	case "":
	case engine.PreActionVolumeExport:
		s += "  (tarball to staging first)"
	default:
		s += "  (pre: " + a.PreAction + ")"
	}
	return s
}
