package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTrackers(t *testing.T) {
	raw := `
# Comment line
udp://tracker.example.com:1337/announce
http://tracker.example.org:80/announce

   
udp://127.0.0.1:6969/announce
udp://tracker.example.com:1337/announce # duplicate
invalid://tracker.com
`
	trackers := parseTrackers(raw)
	if len(trackers) != 3 {
		t.Fatalf("expected 3 trackers, got %d: %v", len(trackers), trackers)
	}

	expected := []string{
		"udp://tracker.example.com:1337/announce",
		"http://tracker.example.org:80/announce",
		"udp://127.0.0.1:6969/announce",
	}

	for i, tr := range trackers {
		if tr != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, tr)
		}
	}
}

func TestToTiers(t *testing.T) {
	trackers := []string{"udp://tr1:1337", "udp://tr2:1337"}
	tiers := toTiers(trackers)
	if len(tiers) != 2 {
		t.Fatalf("expected 2 tiers, got %d", len(tiers))
	}
	if len(tiers[0]) != 1 || tiers[0][0] != trackers[0] {
		t.Errorf("unexpected tier 0: %v", tiers[0])
	}
	if len(tiers[1]) != 1 || tiers[1][0] != trackers[1] {
		t.Errorf("unexpected tier 1: %v", tiers[1])
	}
}

func TestTrackerManagerFallback(t *testing.T) {
	tmpDir := t.TempDir()
	tm := NewTrackerManager(tmpDir)

	tiers := tm.GetTrackers()
	if len(tiers) != len(DefaultTrackers) {
		t.Fatalf("expected %d default trackers, got %d", len(DefaultTrackers), len(tiers))
	}

	// Verify file was written
	path := filepath.Join(tmpDir, "trackers.txt")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected trackers.txt to be created: %v", err)
	}
}
