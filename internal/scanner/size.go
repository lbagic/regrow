package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// DirSize measures disk usage of path in bytes, du-style: physical
// blocks, not logical file length, so sparse files and APFS clones
// report what deletion actually reclaims (research/02 §15,
// "dedup-aware sizing"). Symlinks are not followed. Unreadable
// subtrees are skipped, not fatal: a partial size beats no size.
// Cancelling ctx stops the walk mid-tree — hung filesystems (network
// mounts) can't wedge a scan — and returns the partial total with
// ctx's error, the one walk failure DirSize does report.
func DirSize(ctx context.Context, path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return physicalSize(info), nil
	}
	var total int64
	// Walk errors are never fatal, root included: TCC-protected dirs
	// (~/.Trash, CoreSpotlight) Lstat fine but refuse ReadDir without
	// Full Disk Access. The target exists — report it with whatever
	// size was measurable, exactly like `du 2>/dev/null`.
	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		total += physicalSize(fi)
		return nil
	})
	return total, walkErr
}

// physicalSize prefers allocated blocks (512-byte units, the stat
// contract on darwin and linux) and falls back to logical size.
func physicalSize(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}
