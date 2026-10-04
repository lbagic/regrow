package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/lbagic/regrow/internal/engine"
)

// APFS purgeable space (Prompt H, plans/2026-07-13-doctor-phantom.md
// D3): the Finder-vs-df gap. Foundation's
// NSURLVolumeAvailableCapacityForImportantUsageKey is the
// purgeable-inclusive number Finder shows; NSURLVolumeAvailableCapacityKey
// is the statfs number df shows; purgeable = the difference. The JXA
// ObjC bridge reads both in-process — no Apple events (so no TCC
// automation prompt) and no CGo (keeps CGO_ENABLED=0 release builds).
// diskutil and system_profiler expose only container-free (verified
// live 2026-07-13) — they cannot see purgeable.
const purgeableJXA = `function run(argv) {
	ObjC.import("Foundation");
	const url = $.NSURL.fileURLWithPath(argv[0]);
	const vals = url.resourceValuesForKeysError(
		$([$.NSURLVolumeAvailableCapacityForImportantUsageKey, $.NSURLVolumeAvailableCapacityKey]), null);
	if (vals.isNil()) return "{}";
	return JSON.stringify({
		important: ObjC.unwrap(vals.objectForKey($.NSURLVolumeAvailableCapacityForImportantUsageKey)),
		available: ObjC.unwrap(vals.objectForKey($.NSURLVolumeAvailableCapacityKey)),
	});
}`

func queryAPFSPurgeable(ctx context.Context) ([]engine.Item, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	volume := "/System/Volumes/Data"
	if _, err := os.Stat(volume); err != nil {
		volume = "/"
	}
	out, ok, err := runTool(ctx, "osascript", "-l", "JavaScript", "-e", purgeableJXA, volume)
	if !ok || err != nil {
		return nil, err
	}
	var v struct {
		Important int64 `json:"important"`
		Available int64 `json:"available"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("purgeable probe output: %w", err)
	}
	purgeable := v.Important - v.Available
	if purgeable <= 0 {
		return nil, nil
	}
	return []engine.Item{{
		Label: fmt.Sprintf("purgeable on %s — Finder counts it as free, df does not", volume),
		Arg:   volume,
		Bytes: purgeable,
	}}, nil
}
