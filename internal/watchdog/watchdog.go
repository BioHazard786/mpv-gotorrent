package watchdog

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
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

// Start begins monitoring signals and parent process death.
func (w *Watchdog) Start() {
	slog.Debug("watchdog started", "parent_pid", w.parentPID)

	// 1. Universal OS Signal handler (SIGINT, SIGTERM)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigChan:
			slog.Info("received termination signal", "signal", sig.String())
			w.triggerExit()
		case <-w.ctx.Done():
		}
	}()

	// 2. Platform-specific parent process death detection
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
