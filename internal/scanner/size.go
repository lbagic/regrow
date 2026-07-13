package scanner

import (
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
func DirSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return physicalSize(info), nil
	}
	var total int64
	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == path {
				return err
			}
			return nil // skip unreadable entries
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		total += physicalSize(fi)
		return nil
	})
	if walkErr != nil {
		return total, walkErr
	}
	return total, nil
}

// physicalSize prefers allocated blocks (512-byte units, the stat
// contract on darwin and linux) and falls back to logical size.
func physicalSize(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}
