//go:build windows

package watchdog

import (
	"context"

	"golang.org/x/sys/windows"
)

func setupOSWatchdog(ctx context.Context, parentPID int, onExit func()) {
	// Open parent process with SYNCHRONIZE rights to wait for its termination
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if err != nil {
		// Parent process may have already died
		onExit()
		return
	}
	defer windows.CloseHandle(handle)

	// Wait in background for parent process object to be signaled (terminated)
	done := make(chan struct{})
	go func() {
		event, _ := windows.WaitForSingleObject(handle, windows.INFINITE)
		if event == windows.WAIT_OBJECT_0 {
			onExit()
		}
		close(done)
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}
}
