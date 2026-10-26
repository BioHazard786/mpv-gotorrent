package engine

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// writeTorrentFor builds a .torrent file describing the file or directory at contentPath.
func writeTorrentFor(t *testing.T, contentPath string) string {
	t.Helper()
	info := metainfo.Info{PieceLength: 64 << 10}
	if err := info.BuildFromFilePath(contentPath); err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{}
	var err error
	if mi.InfoBytes, err = bencode.Marshal(info); err != nil {
		t.Fatal(err)
	}
	torrentPath := filepath.Join(t.TempDir(), "test.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := mi.Write(f); err != nil {
		t.Fatal(err)
	}
	return torrentPath
}

func TestKeepExportsFileFromMultiFileTorrent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Seed the cache dir with complete data laid out the way anacrolix stores it
	root := t.TempDir()
	season := filepath.Join(root, "Show", "Season 1")
	if err := os.MkdirAll(season, 0755); err != nil {
		t.Fatal(err)
	}
	ep1 := bytes.Repeat([]byte("a"), 300<<10)
	if err := os.WriteFile(filepath.Join(season, "ep1.mkv"), ep1, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(season, "ep2.mkv"), bytes.Repeat([]byte("b"), 200<<10), 0644); err != nil {
		t.Fatal(err)
	}
	torrentPath := writeTorrentFor(t, filepath.Join(root, "Show"))

	eng, err := NewEngine(Config{DataDir: root, KeepFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.AddSource(context.Background(), torrentPath); err != nil {
		t.Fatal(err)
	}

	keepDir := t.TempDir()
	returned := make(chan KeepStatus, 1)
	go func() {
		status, _ := eng.KeepCurrent("current", keepDir, 0)
		returned <- status
	}()
	select {
	case status := <-returned:
		if status.Status != "keeping" {
			t.Fatalf("expected keeping status, got %+v", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("KeepCurrent deadlocked")
	}

	select {
	case <-eng.WaitKeepComplete():
	case <-time.After(15 * time.Second):
		t.Fatal("keep never completed")
	}

	got, err := os.ReadFile(filepath.Join(keepDir, "ep1.mkv"))
	if err != nil {
		t.Fatalf("kept file missing: %v", err)
	}
	if !bytes.Equal(got, ep1) {
		t.Error("kept file content mismatch")
	}

	if status, _ := eng.KeepCurrent("current", keepDir, 0); status.Status != "completed" {
		t.Errorf("expected completed status on repeat keep, got %q", status.Status)
	}
}

func TestKeepRejectedInRAMMode(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	content := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(content, make([]byte, 100<<10), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(Config{MaxRAMMB: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.AddSource(context.Background(), writeTorrentFor(t, content)); err != nil {
		t.Fatal(err)
	}

	status, err := eng.KeepCurrent("current", t.TempDir(), 0)
	if err == nil || status.Status != "error" {
		t.Errorf("expected keep to be rejected in RAM mode, got %+v", status)
	}
}

func TestFetchTorrentURLFollowsMagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusFound)
	}))
	defer srv.Close()

	e := &Engine{}
	mi, got, err := e.fetchTorrentURL(context.Background(), srv.URL+"/dl/1")
	if err != nil {
		t.Fatalf("fetchTorrentURL failed: %v", err)
	}
	if mi != nil || got != magnet {
		t.Errorf("expected magnet %q, got %q (metainfo %v)", magnet, got, mi)
	}
}

func TestCleanStaleSessionDirsKeepsLiveSessions(t *testing.T) {
	parent := t.TempDir()
	stale := filepath.Join(parent, "mpv-gotorrent-stale")
	live := filepath.Join(parent, "mpv-gotorrent-live")
	for _, d := range []string{stale, live} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		touchHeartbeat(d)
	}

	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(stale, heartbeatFile), old, old)
	// A long-running session: old directory, fresh heartbeat
	_ = os.Chtimes(live, old, old)

	cleanStaleSessionDirs("", parent)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("expected stale session dir to be removed")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("live session dir was removed")
	}
}
