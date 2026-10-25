package streamer

import (
	"strings"
	"testing"

	"github.com/biohazard786/mpv-gotorrent/internal/playlist"
)

func TestListenTCPWithFallback(t *testing.T) {
	listener, port, err := listenTCPWithFallback(8989)
	if err != nil {
		t.Fatalf("listenTCPWithFallback failed: %v", err)
	}
	defer listener.Close()

	if port < 8989 {
		t.Errorf("expected port >= 8989, got %d", port)
	}
}

func TestM3UPlaylistGeneration(t *testing.T) {
	s := &Server{
		videoFiles: []playlist.FileItem{
			{
				Index:     0,
				Name:      "Episode 1.mkv",
				StreamURL: "http://127.0.0.1:8989/torrent/abc123/0/Episode%201.mkv",
			},
			{
				Index:     1,
				Name:      "Episode 2.mkv",
				StreamURL: "http://127.0.0.1:8989/torrent/abc123/1/Episode%202.mkv",
			},
		},
	}

	m3u := s.M3UPlaylist()
	if !strings.HasPrefix(m3u, "#EXTM3U\n") {
		t.Fatalf("expected #EXTM3U header, got %q", m3u)
	}

	if !strings.Contains(m3u, "#EXTINF:-1,Episode 1\n") {
		t.Errorf("expected clean title 'Episode 1', got %q", m3u)
	}

	if !strings.Contains(m3u, "http://127.0.0.1:8989/torrent/abc123/0/Episode%201.mkv") {
		t.Errorf("expected stream URL in m3u, got %q", m3u)
	}
}
