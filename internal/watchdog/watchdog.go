package watchdog

import (
	"context"
	"log/slog"
	"sync"
)

// Watchdog oversees parent process vitality across platforms.
type Watchdog struct {
	parentPID int
	onExit    func()
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
}

// New creates a new cross-platform Watchdog.
func New(parentPID int, onExit func()) *Watchdog {
	ctx, cancel := context.WithCancel(context.Background())
	return &Watchdog{
		parentPID: parentPID,
		onExit:    onExit,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins monitoring parent process death. Signals are left to the caller, which needs to
// tell "mpv exited" (may continue a kept download) apart from "asked to stop" (always exits).
func (w *Watchdog) Start() {
	slog.Debug("watchdog started", "parent_pid", w.parentPID)

	if w.parentPID > 1 {
		go setupOSWatchdog(w.ctx, w.parentPID, func() {
			slog.Info("parent process termination detected by OS monitor", "parent_pid", w.parentPID)
			w.triggerExit()
		})
	}
}

// Context returns the watchdog context which is cancelled on exit.
func (w *Watchdog) Context() context.Context {
	return w.ctx
}

func (w *Watchdog) triggerExit() {
	w.once.Do(func() {
		if w.onExit != nil {
			w.onExit()
		}
		w.cancel()
	})
}
