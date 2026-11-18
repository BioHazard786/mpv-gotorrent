//go:build !linux && !darwin && !freebsd && !windows

package engine

// freeDiskBytes is unknown on this platform, so no low-space warning is shown.
func freeDiskBytes(string) (uint64, bool) { return 0, false }
