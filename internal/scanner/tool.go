package scanner

import (
	"context"
	"os/exec"

	"github.com/lbagic/regrow/internal/engine"
)

// ToolQuery enumerates targets only a steward tool can see (docker
// system df, simctl list, hf scan-cache, ollama list). Queries live
// in code because their output parsing is per-tool; rules reference
// them by name via tool_query.
type ToolQuery func(ctx context.Context) ([]engine.Item, error)

// DefaultQueries returns the built-in query registry. Phase 3G adds
// hf and ollama here.
func DefaultQueries() map[string]ToolQuery {
	return map[string]ToolQuery{
		"docker-reclaimable":         queryDockerReclaimable,
		"docker-volumes":             queryDockerVolumes,
		"simctl-devices":             querySimctlDevices,
		"simctl-devices-unavailable": querySimctlDevicesUnavailable,
		"simctl-runtimes":            querySimctlRuntimes,
		"tm-snapshots":               queryTMSnapshots,
	}
}

// runTool executes a tool and returns stdout. A tool missing from
// PATH means the rule does not apply to this machine: (nil, false)
// with no error, so laptops without docker or Xcode stay quiet.
func runTool(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, false, nil
	}
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
