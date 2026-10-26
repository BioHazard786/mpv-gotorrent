//go:build linux

package watchdog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// setupOSWatchdog polls parentPID directly. PR_SET_PDEATHSIG isn't used: mpv starts the companion
// detached (so mpv can't SIGKILL it), which means the parent the kernel sees is not mpv.
func setupOSWatchdog(ctx context.Context, parentPID int, onExit func()) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if processGone(parentPID) {
				onExit()
				return
			}
		}
	}
}

// processGone reports whether pid has exited. A crashed process whose parent hasn't reaped it
// lingers as a zombie that kill(pid, 0) still sees, so /proc state Z (zombie) or X (dead) also
// counts as gone.
func processGone(pid int) bool {
	// EPERM means the process exists but belongs to someone else; only ESRCH means gone.
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}

	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		return false // /proc unavailable; rely on kill
	}
	// Format is "pid (comm) state ...", and comm may itself contain spaces or parentheses
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return false
	}
	state := stat[i+2]
	return state == 'Z' || state == 'X'
}
