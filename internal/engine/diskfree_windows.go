//go:build windows

package engine

import "golang.org/x/sys/windows"

// freeDiskBytes reports the bytes available to this user on the volume holding dir.
func freeDiskBytes(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, false
	}
	return free, true
}
