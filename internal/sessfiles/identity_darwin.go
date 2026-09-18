//go:build darwin

package sessfiles

import (
	"os"
	"syscall"
)

// fileIdentity returns (device, inode, ctime) from a Darwin stat result.
func fileIdentity(fi os.FileInfo) (dev, ino uint64, ctimeNS int64, ok bool) {
	st, okCast := fi.Sys().(*syscall.Stat_t)
	if !okCast {
		return 0, 0, 0, false
	}
	return uint64(st.Dev), st.Ino, st.Ctimespec.Nano(), true
}
