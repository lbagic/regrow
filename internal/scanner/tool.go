package scanner

import (
	"context"

	"github.com/lbagic/regrow/internal/engine"
)

// ToolQuery enumerates targets only a steward tool can see (docker
// system df, simctl list, hf scan-cache, ollama list). Queries live
// in code because their output parsing is per-tool; rules reference
// them by name via tool_query.
type ToolQuery func(ctx context.Context) ([]engine.Item, error)

// DefaultQueries returns the built-in query registry. Phases 1E/3G
// register docker, simctl, hf, and ollama here; the engine contract
// is already fixed.
func DefaultQueries() map[string]ToolQuery {
	return map[string]ToolQuery{}
}
