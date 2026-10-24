package watchdog

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchdogTriggerExitIdempotent(t *testing.T) {
	var count int32
	onExit := func() {
		atomic.AddInt32(&count, 1)
	}

	wd := New(0, onExit)

	// Call triggerExit multiple times
	wd.triggerExit()
	wd.triggerExit()
	wd.triggerExit()

	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected onExit to be called exactly once, got %d", count)
	}

	select {
	case <-wd.Context().Done():
		// Context should be cancelled
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected watchdog context to be cancelled")
	}
}
