package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// Docker Desktop keeps its own settings in its group container
// (settings-store.json since 4.35, settings.json before) and the
// engine's in ~/.docker/daemon.json. Both are plain files: neither
// check talks to the daemon.
const (
	dockerSettingsDir  = "~/Library/Group Containers/group.com.docker"
	dockerDaemonConfig = "~/.docker/daemon.json"
)

// dockerVMMemory reads the VM's memory limit. Docker Desktop writes
// only settings that differ from its defaults, so a missing key means
// the default: up to half of the machine's memory.
func (c *causeChecks) dockerVMMemory(ctx context.Context, _ engine.Rule) (engine.Verdict, string) {
	for _, name := range []string{"settings-store.json", "settings.json"} {
		path := c.host.ExpandPath(dockerSettingsDir + "/" + name)
		data, err := c.read(ctx, path)
		if absent(err) {
			continue
		}
		shown := tilde(c.host, path)
		if err != nil {
			return engine.VerdictUnknown, shown + " is " + engine.UnreadableNote
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(data, &settings); err != nil {
			return engine.VerdictUnknown, shown + " is not a JSON object"
		}
		total := c.totalMemory()
		for key, raw := range settings {
			// MemoryMiB in settings-store.json, memoryMiB in settings.json.
			if !strings.EqualFold(key, "memoryMiB") {
				continue
			}
			var mib float64
			if err := json.Unmarshal(raw, &mib); err != nil {
				return engine.VerdictUnknown, fmt.Sprintf("%s in %s is not a number", key, shown)
			}
			if mib <= 0 {
				break
			}
			detail := "capped at " + engine.HumanBytes(int64(mib)<<20)
			if total > 0 {
				detail += " of this Mac's " + engine.HumanBytes(total)
			}
			return engine.VerdictNormal, detail
		}
		detail := "no memory limit in Docker Desktop's settings, so its default applies: half of this Mac's memory"
		if total > 0 {
			detail += fmt.Sprintf(" (%s of %s)", engine.HumanBytes(total/2), engine.HumanBytes(total))
		}
		return engine.VerdictFlagged, detail
	}
	return engine.VerdictNormal, "does not apply: no Docker Desktop settings on this machine"
}

func (c *causeChecks) totalMemory() int64 {
	if c.memory != nil {
		return c.memory()
	}
	return hostMemory()
}

// dockerBuildCacheLimit reads builder.gc from the engine's config.
// Enabled with no size still collects, under Docker's default limits;
// only a collection that is off or never switched on has no limit.
func (c *causeChecks) dockerBuildCacheLimit(ctx context.Context, _ engine.Rule) (engine.Verdict, string) {
	path := c.host.ExpandPath(dockerDaemonConfig)
	shown := tilde(c.host, path)
	data, err := c.read(ctx, path)
	switch {
	case absent(err):
		return engine.VerdictNormal, "does not apply: no " + shown + " on this machine"
	case err != nil:
		return engine.VerdictUnknown, shown + " is " + engine.UnreadableNote
	}
	var cfg struct {
		Builder struct {
			GC struct {
				Enabled              *bool             `json:"enabled"`
				DefaultKeepStorage   string            `json:"defaultKeepStorage"`
				DefaultReservedSpace string            `json:"defaultReservedSpace"`
				DefaultMaxUsedSpace  string            `json:"defaultMaxUsedSpace"`
				DefaultMinFreeSpace  string            `json:"defaultMinFreeSpace"`
				Policy               []json.RawMessage `json:"policy"`
			} `json:"gc"`
		} `json:"builder"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return engine.VerdictUnknown, shown + " is not a JSON object"
	}
	gc := cfg.Builder.GC
	switch {
	case gc.Enabled == nil:
		return engine.VerdictFlagged, shown + " does not switch build-cache garbage collection on (builder.gc.enabled), so it sets no limit"
	case !*gc.Enabled:
		return engine.VerdictFlagged, shown + " switches build-cache garbage collection off (builder.gc.enabled), so the cache has no limit"
	}
	for _, size := range []struct{ key, value string }{
		{"defaultKeepStorage", gc.DefaultKeepStorage},
		{"defaultReservedSpace", gc.DefaultReservedSpace},
		{"defaultMaxUsedSpace", gc.DefaultMaxUsedSpace},
		{"defaultMinFreeSpace", gc.DefaultMinFreeSpace},
	} {
		if size.value != "" {
			return engine.VerdictNormal, fmt.Sprintf("garbage collection on: builder.gc.%s is %s", size.key, size.value)
		}
	}
	if len(gc.Policy) > 0 {
		return engine.VerdictNormal, "garbage collection on, under the policy in builder.gc.policy"
	}
	return engine.VerdictNormal, "garbage collection on, under Docker's default limits"
}
