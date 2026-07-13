//go:build linux

package scanner

import (
	"io/fs"
	"syscall"
	"time"
)

// atime returns the file's last-access time, zero when the platform
// stat does not carry one.
func atime(fi fs.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atim.Sec, st.Atim.Nsec)
	}
	return time.Time{}
}
