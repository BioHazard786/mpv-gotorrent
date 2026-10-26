//go:build darwin

package watchdog

import (
	"context"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func setupOSWatchdog(ctx context.Context, parentPID int, onExit func()) {
	kq, err := unix.Kqueue()
	if err == nil {
		defer unix.Close(kq)
		ev := unix.Kevent_t{
			Ident:  uint64(parentPID),
			Filter: unix.EVFILT_PROC,
			Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT,
			Fflags: unix.NOTE_EXIT,
		}

		go func() {
			var events [1]unix.Kevent_t
			n, err := unix.Kevent(kq, []unix.Kevent_t{ev}, events[:], nil)
			if n > 0 && err == nil {
				onExit()
			}
		}()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := syscall.Kill(parentPID, 0); err == syscall.ESRCH {
				onExit()
				return
			}
		}
	}
}
