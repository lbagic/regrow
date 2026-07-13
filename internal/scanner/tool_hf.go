package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// HuggingFace hub cache (docs/research/02 §9). Enumeration walks the
// documented cache layout directly instead of shelling out to
// `hf`/`huggingface-cli`: the CLI is usually venv-bound (a machine with
// 100GB of models often has no `hf` on PATH), and the cache is plain
// filesystem — no DB to desync. Blobs live *per repo* (snapshots
// symlink into the sibling blobs/ dir), so trashing a whole repo dir is
// exactly what `hf cache rm` does, with undo on top. Sizes come from
// DirSize (physical blocks; symlinks not followed), so a blob shared by
// several snapshots of one repo counts once — dedup-aware by
// construction.

func queryHFHub(ctx context.Context) ([]engine.Item, error) {
	dir, ok := hfHubDir()
	if !ok {
		return nil, nil
	}
	return scanHFHub(ctx, dir)
}

// hfHubDir resolves the hub cache location the way huggingface_hub
// does: HF_HUB_CACHE > HUGGINGFACE_HUB_CACHE (legacy) > $HF_HOME/hub >
// ~/.cache/huggingface/hub. A missing dir means the rule does not
// apply to this machine.
func hfHubDir() (string, bool) {
	for _, env := range []string{"HF_HUB_CACHE", "HUGGINGFACE_HUB_CACHE"} {
		if v := os.Getenv(env); v != "" {
			return v, isDir(v)
		}
	}
	base := os.Getenv("HF_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		base = filepath.Join(home, ".cache", "huggingface")
	}
	p := filepath.Join(base, "hub")
	return p, isDir(p)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func scanHFHub(ctx context.Context, dir string) ([]engine.Item, error) {
	entries, err := os.ReadDir(dir) // sorted by name: deterministic
	if err != nil {
		return nil, err
	}
	var items []engine.Item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repoType, repoID, ok := decodeHFRepoDir(e.Name())
		if !ok {
			continue // version.txt, .locks, unknown layouts
		}
		repoPath := filepath.Join(dir, e.Name())
		bytes, err := DirSize(ctx, repoPath)
		if err != nil {
			return items, err // only ctx cancellation reaches here
		}
		label := repoID
		if repoType != "model" {
			label += " (" + repoType + ")"
		}
		items = append(items, engine.Item{
			Path:     repoPath,
			Label:    label,
			Arg:      repoType + "/" + repoID,
			Bytes:    bytes,
			LastUsed: hfLastUsed(repoPath),
		})
	}
	return items, nil
}

// decodeHFRepoDir reverses huggingface_hub's folder naming:
// "models--meta-llama--Llama-3-8B" → ("model", "meta-llama/Llama-3-8B").
// Repo names cannot contain "--", so the split is unambiguous.
func decodeHFRepoDir(name string) (repoType, repoID string, ok bool) {
	segs := strings.Split(name, "--")
	if len(segs) < 2 {
		return "", "", false
	}
	switch segs[0] {
	case "models", "datasets", "spaces":
		return strings.TrimSuffix(segs[0], "s"), strings.Join(segs[1:], "/"), true
	}
	return "", "", false
}

// hfLastUsed reports when the repo was last read: the max atime/mtime
// across snapshot files, Stat'd through the symlinks so the underlying
// blobs answer — the same signal `scan-cache` calls last_accessed.
// Falls back to the repo dir's mtime when there are no snapshots.
func hfLastUsed(repoPath string) time.Time {
	var last time.Time
	_ = filepath.WalkDir(filepath.Join(repoPath, "snapshots"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		fi, err := os.Stat(p) // follows the snapshot→blob symlink
		if err != nil {
			return nil
		}
		for _, t := range []time.Time{atime(fi), fi.ModTime()} {
			if t.After(last) {
				last = t
			}
		}
		return nil
	})
	if last.IsZero() {
		if fi, err := os.Stat(repoPath); err == nil {
			last = fi.ModTime()
		}
	}
	return last
}
