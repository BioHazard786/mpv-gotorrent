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

	// A second episode can be kept in the same session
	if status, _ := eng.KeepCurrent("current", keepDir, 1); status.Status != "keeping" {
		t.Fatalf("expected a second file to start keeping, got %+v", status)
	}
	select {
	case <-eng.WaitKeepComplete():
	case <-time.After(15 * time.Second):
		t.Fatal("second keep never completed")
	}
	if !fileExists(filepath.Join(keepDir, "ep2.mkv")) {
		t.Error("second kept file missing")
	}
	if !eng.IsKeepCompleted() {
		t.Error("expected every keep to be completed")
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

// newWindowEngine starts a disk-mode engine with a 2-file cache window over a three-episode pack,
// with every episode present in the session cache both as a finished and a .part file.
func newWindowEngine(t *testing.T) (*Engine, func(name string) string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	content := filepath.Join(t.TempDir(), "Show")
	if err := os.MkdirAll(content, 0755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ep1.mkv", "ep2.mkv", "ep3.mkv"} {
		if err := os.WriteFile(filepath.Join(content, n), bytes.Repeat([]byte(n), 100<<10), 0644); err != nil {
			t.Fatal(err)
		}
	}

	eng, err := NewEngine(Config{MaxCachedFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	if _, err := eng.AddSource(context.Background(), writeTorrentFor(t, content)); err != nil {
		t.Fatal(err)
	}

	cached := func(n string) string { return filepath.Join(eng.DataDir(), "Show", n) }
	for _, n := range []string{"ep1.mkv", "ep2.mkv", "ep3.mkv"} {
		if err := os.MkdirAll(filepath.Dir(cached(n)), 0755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{cached(n), cached(n) + ".part"} {
			if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return eng, cached
}

// onDisk reports whether a cached file still exists. Marking a neighbour's shared piece incomplete
// may rename it back to .part, so either name counts.
func onDisk(cached func(string) string, n string) bool {
	return fileExists(cached(n)) || fileExists(cached(n+".part"))
}

func TestNoteStreamedDeletesFilesOutsideWindow(t *testing.T) {
	eng, cached := newWindowEngine(t)

	for _, idx := range []int{0, 1, 0, 2} { // replaying ep1 refreshes it, so ep2 is oldest
		eng.NoteStreamed(idx)
	}
	eng.freeing.Wait()

	if onDisk(cached, "ep2.mkv") {
		t.Error("oldest file was not deleted")
	}
	for _, n := range []string{"ep1.mkv", "ep3.mkv"} {
		if !onDisk(cached, n) {
			t.Errorf("%s inside the window was deleted", n)
		}
	}
}

func TestNoteStreamedSparesKeptFile(t *testing.T) {
	eng, cached := newWindowEngine(t)
	eng.keeps = append(eng.keeps, &keepJob{file: eng.Torrent().Files()[0], done: make(chan struct{})})

	for _, idx := range []int{0, 1, 2} {
		eng.NoteStreamed(idx)
	}
	eng.freeing.Wait()

	if !onDisk(cached, "ep1.mkv") {
		t.Error("kept file was deleted after leaving the window")
	}
}

func TestSeedRatioPausesAndResumesUpload(t *testing.T) {
	eng, _ := newWindowEngine(t)
	eng.seedRatio = 1.5

	paused := func() bool {
		eng.mu.RLock()
		defer eng.mu.RUnlock()
		return eng.uploadPaused
	}
	eng.enforceSeedRatio(100, 149)
	if paused() {
		t.Fatal("paused below the ratio")
	}
	eng.enforceSeedRatio(100, 150)
	if !paused() {
		t.Fatal("not paused at the ratio")
	}
	eng.enforceSeedRatio(200, 150) // more downloaded raises the cap again
	if paused() {
		t.Fatal("still paused after dropping below the ratio")
	}
}

func TestFreeDiskBytesReadsTempDir(t *testing.T) {
	free, ok := freeDiskBytes(t.TempDir())
	if ok && free == 0 {
		t.Error("reported zero free bytes on a writable temp dir")
	}
}
