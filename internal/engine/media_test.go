package engine

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
)

func TestReadaheadFollowsBitrate(t *testing.T) {
	e := &Engine{fileBitrates: map[int]int64{}}
	if got := e.Readahead(0); got != defaultReadahead {
		t.Errorf("expected default readahead before any bitrate is known, got %d", got)
	}

	cases := []struct {
		name string
		bps  int64
		want int64
	}{
		{"720p web encode clamps to minimum", 300 << 10, minReadahead},
		{"1080p bluray", 2 << 20, 2 << 20 * readaheadSeconds},
		{"4K remux clamps to maximum", 12 << 20, maxReadahead},
	}
	for _, c := range cases {
		e.fileBitrates[0] = c.bps
		if got := e.Readahead(0); got != c.want {
			t.Errorf("%s: expected %d, got %d", c.name, c.want, got)
		}
	}

	e.lastBitrate = 2 << 20
	if got := e.Readahead(5); got != 2<<20*readaheadSeconds {
		t.Errorf("expected file without a duration to use the last bitrate, got %d", got)
	}

	e.ramStorage = &RAMStorageClient{}
	e.maxRAMMB = 128
	if got := e.Readahead(5); got != 32<<20 {
		t.Errorf("expected readahead capped to a quarter of RAM, got %d", got)
	}
}

func TestSetFileDurationRejectsBadDurations(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	content := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(content, make([]byte, 100<<10), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.AddSource(context.Background(), writeTorrentFor(t, content)); err != nil {
		t.Fatal(err)
	}

	for _, d := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := eng.SetFileDuration(0, d); err == nil {
			t.Errorf("expected duration %v to be rejected", d)
		}
	}
	if _, err := eng.SetFileDuration(3, 60); err == nil {
		t.Error("expected out of range file index to be rejected")
	}
	if bps, err := eng.SetFileDuration(0, 10); err != nil || bps != 10<<10 {
		t.Errorf("expected 10 KiB/s, got %d (%v)", bps, err)
	}
}

func TestPrefetchFileQueuesOnlyThatFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	show := filepath.Join(t.TempDir(), "Show")
	if err := os.MkdirAll(show, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ep1.mkv", "ep2.mkv", "ep3.mkv"} {
		if err := os.WriteFile(filepath.Join(show, name), make([]byte, 640<<10), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// No data in the session dir, so every piece is still missing
	eng, err := NewEngine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	tor, err := eng.AddSource(context.Background(), writeTorrentFor(t, show))
	if err != nil {
		t.Fatal(err)
	}

	queued, err := eng.PrefetchFile(1)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 640<<10 {
		t.Errorf("expected the whole small file to be queued, got %d bytes", queued)
	}

	// Effective priority reads as none until anacrolix has checked the pieces against storage
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !tor.Piece(tor.NumPieces() - 1).State().Ok {
		time.Sleep(20 * time.Millisecond)
	}

	for i, f := range tor.Files() {
		for p := f.BeginPieceIndex(); p < f.EndPieceIndex(); p++ {
			prio := tor.Piece(p).State().Priority
			if i == 1 && prio < torrent.PiecePriorityNormal {
				t.Errorf("piece %d of next file not queued", p)
			}
			if i != 1 && prio != torrent.PiecePriorityNone {
				t.Errorf("piece %d of file %d unexpectedly queued with priority %v", p, i, prio)
			}
		}
	}
}
