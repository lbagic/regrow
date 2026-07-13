package scanner

import (
	"context"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// queryTMSnapshots lists local Time Machine snapshots. APFS does not
// expose per-snapshot sizes cheaply, so items carry zero bytes: the
// win shows up as purgeable space after thinning, and the rule's
// regen story explains that.
func queryTMSnapshots(ctx context.Context) ([]engine.Item, error) {
	out, ok, err := runTool(ctx, "tmutil", "listlocalsnapshots", "/")
	if !ok || err != nil {
		return nil, nil
	}
	return parseTMSnapshots(string(out)), nil
}

func parseTMSnapshots(out string) []engine.Item {
	var items []engine.Item
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "com.apple.TimeMachine.") {
			items = append(items, engine.Item{Label: line})
		}
	}
	return items
}
