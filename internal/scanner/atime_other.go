//go:build !darwin && !linux

package scanner

import (
	"io/fs"
	"time"
)

// atime is unavailable on this platform; callers fall back to mtime.
func atime(fs.FileInfo) time.Time { return time.Time{} }
