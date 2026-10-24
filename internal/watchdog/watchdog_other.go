//go:build !linux && !darwin && !windows

package watchdog

import (
	"context"
	"os"
	"syscall"
	"time"
)

func setupOSWatchdog(ctx context.Context, parentPID int, onExit func()) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p, err := os.FindProcess(parentPID)
			if err != nil {
				onExit()
				return
			}
			if err := p.Signal(syscall.Signal(0)); err != nil {
				onExit()
				return
			}
		}
	}
}
