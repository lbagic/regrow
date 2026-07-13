package scanner

import (
	"context"
	"strconv"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// `docker system df` is the only reclaimable-space view Docker gives
// without walking the VM. Items are per-type aggregates because the
// daemon does not track last-used for images (only build cache has
// it, via df -v) — per-image aging is a later extension.

func queryDockerReclaimable(ctx context.Context) ([]engine.Item, error) {
	rows, err := dockerDF(ctx)
	if err != nil || rows == nil {
		return nil, err
	}
	var items []engine.Item
	for _, want := range []struct{ dfType, label string }{
		{"Images", "dangling images"},
		{"Containers", "stopped containers"},
		{"Build Cache", "build cache"},
	} {
		if b := rows[want.dfType]; b > 0 {
			items = append(items, engine.Item{Label: want.label, Bytes: b})
		}
	}
	return items, nil
}

func queryDockerVolumes(ctx context.Context) ([]engine.Item, error) {
	rows, err := dockerDF(ctx)
	if err != nil || rows == nil {
		return nil, err
	}
	if b := rows["Local Volumes"]; b > 0 {
		return []engine.Item{{Label: "unused volumes", Bytes: b}}, nil
	}
	return nil, nil
}

// dockerDF returns reclaimable bytes by df type. Docker installed but
// daemon not running is the everyday case on laptops, so any exec
// failure means "does not apply right now", not a scan error.
func dockerDF(ctx context.Context) (map[string]int64, error) {
	out, ok, err := runTool(ctx, "docker", "system", "df", "--format", "{{.Type}}\t{{.Reclaimable}}")
	if !ok || err != nil {
		return nil, nil
	}
	return parseDockerDF(string(out)), nil
}

func parseDockerDF(out string) map[string]int64 {
	rows := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		typ, size, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		if b, ok := parseDockerSize(strings.TrimSpace(size)); ok {
			rows[strings.TrimSpace(typ)] = b
		}
	}
	return rows
}

// parseDockerSize parses go-units output like "1.5GB (50%)" or "0B".
// Docker uses SI units: kB = 1000 bytes.
func parseDockerSize(s string) (int64, bool) {
	if before, _, found := strings.Cut(s, " "); found {
		s = before
	}
	i := strings.IndexFunc(s, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	if i <= 0 {
		return 0, false
	}
	val, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	mult, ok := map[string]float64{
		"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	}[s[i:]]
	if !ok {
		return 0, false
	}
	return int64(val * mult), true
}
