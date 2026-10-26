//go:build linux

package watchdog

import (
	"os/exec"
	"testing"
	"time"
)

func TestProcessGoneDetectsZombie(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	if processGone(pid) {
		t.Fatal("running process reported as gone")
	}

	// Kill without reaping, leaving a zombie like a crashed mpv under a launcher that never waits
	_ = cmd.Process.Kill()
	deadline := time.Now().Add(2 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatal("zombie process not reported as gone")
		}
		time.Sleep(10 * time.Millisecond)
	}

	_ = cmd.Wait()
	if !processGone(pid) {
		t.Error("reaped process not reported as gone")
	}
}
