//go:build linux

package watchdog

import (
	"context"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func setupOSWatchdog(ctx context.Context, parentPID int, onExit func()) {
	// Instruct Linux kernel to deliver SIGTERM to this process if parent process dies
	_ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGTERM), 0, 0, 0)

	// If parent died before prctl was called
	if os.Getppid() == 1 {
		onExit()
		return
	}

	// Periodic check of parentPID existence
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := syscall.Kill(parentPID, 0); err != nil {
				onExit()
				return
			}
		}
	}
}
