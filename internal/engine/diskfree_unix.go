//go:build linux || darwin || freebsd

package engine

import "golang.org/x/sys/unix"

// freeDiskBytes reports the bytes available to this user on the filesystem holding dir.
func freeDiskBytes(dir string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
