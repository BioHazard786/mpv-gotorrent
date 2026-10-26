package engine

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

func TestRAMStorageBasicAndEviction(t *testing.T) {
	// Create mock metainfo.Info with 4 pieces of 100 bytes each
	info := &metainfo.Info{
		PieceLength: 100,
		Pieces:      make([]byte, 20*4), // 4 SHA-1 hashes (20 bytes each)
		Files: []metainfo.FileInfo{
			{
				Length: 400,
				Path:   []string{"movie.mp4"},
			},
		},
	}

	// Max RAM is 1000 bytes, so the 4 head/tail pieces fit within the half-budget pin cap
	client := NewRAMStorageClient(1000, nil)
	ih := metainfo.Hash{}

	ti, err := client.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatalf("failed to open torrent: %v", err)
	}
	defer ti.Close()

	rt := client.ActiveTorrent()
	if rt == nil {
		t.Fatalf("expected active torrent")
	}

	// Verify pinning: piece 0 and 1 are first pieces, piece 2 and 3 are tail
	// For 4 pieces with 1 file, all 4 are within head/tail window
	if len(rt.pinned) != 4 {
		t.Errorf("expected 4 pinned pieces, got %d", len(rt.pinned))
	}

	// Write data to piece 0
	p0 := rt.Piece(info.Piece(0))
	data0 := bytes.Repeat([]byte{0xAA}, 100)
	n, err := p0.WriteAt(data0, 0)
	if err != nil || n != 100 {
		t.Fatalf("failed WriteAt on piece 0: %v", err)
	}

	// Read back from piece 0
	readBuf := make([]byte, 100)
	n, err = p0.ReadAt(readBuf, 0)
	if err != nil || n != 100 {
		t.Fatalf("failed ReadAt on piece 0: %v", err)
	}
	if !bytes.Equal(readBuf, data0) {
		t.Errorf("read data mismatch")
	}

	// Test MarkComplete
	_ = p0.MarkComplete()
	cmpl := p0.Completion()
	if !cmpl.Ok || !cmpl.Complete {
		t.Errorf("expected piece 0 complete, got %v", cmpl)
	}
}

func TestRAMStoragePinCapLimitsSeasonPacks(t *testing.T) {
	// 10 files of 4 pieces each; pinning them all would need 4000 bytes
	var files []metainfo.FileInfo
	for i := 0; i < 10; i++ {
		files = append(files, metainfo.FileInfo{Length: 400, Path: []string{string(rune('a'+i)) + ".mkv"}})
	}
	info := &metainfo.Info{PieceLength: 100, Pieces: make([]byte, 20*40), Files: files}

	client := NewRAMStorageClient(1000, nil)
	if _, err := client.OpenTorrent(context.Background(), info, metainfo.Hash{}); err != nil {
		t.Fatal(err)
	}
	rt := client.ActiveTorrent()

	if pinnedBytes := int64(len(rt.pinned)) * 100; pinnedBytes > 500 {
		t.Errorf("expected pinned bytes within half the budget, got %d", pinnedBytes)
	}
	if info.Files[0].Path[0] != "a.mkv" {
		t.Errorf("pinning must not reorder the torrent's files, first file is %v", info.Files[0].Path)
	}
}

func TestRAMStorageDoesNotSpinWhenAllPinned(t *testing.T) {
	// 3 files x 2 pieces, all pinned (budget 1200 allows 600 pinned bytes). Shrinking the budget
	// below the pinned total leaves nothing evictable.
	info := &metainfo.Info{PieceLength: 100, Pieces: make([]byte, 20*6), Files: []metainfo.FileInfo{
		{Length: 200, Path: []string{"a.mkv"}},
		{Length: 200, Path: []string{"b.mkv"}},
		{Length: 200, Path: []string{"c.mkv"}},
	}}
	client := NewRAMStorageClient(1200, nil)
	if _, err := client.OpenTorrent(context.Background(), info, metainfo.Hash{}); err != nil {
		t.Fatal(err)
	}
	rt := client.ActiveTorrent()
	rt.maxRAMBytes = 250

	done := make(chan struct{})
	go func() {
		for i := 0; i < 6; i++ {
			_, _ = rt.Piece(info.Piece(i)).WriteAt(make([]byte, 100), 0)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("writes hung when every cached piece is pinned")
	}
}

func TestRAMStorageEvictsCompletePiecesFirst(t *testing.T) {
	// 6 pieces in one file: head (0, 1) and tail (4, 5) are pinned, leaving 2 and 3 evictable.
	info := &metainfo.Info{PieceLength: 100, Pieces: make([]byte, 20*6), Files: []metainfo.FileInfo{
		{Length: 600, Path: []string{"movie.mkv"}},
	}}
	client := NewRAMStorageClient(800, nil)
	if _, err := client.OpenTorrent(context.Background(), info, metainfo.Hash{}); err != nil {
		t.Fatal(err)
	}
	rt := client.ActiveTorrent()
	if rt.pinned[2] || rt.pinned[3] {
		t.Fatalf("expected pieces 2 and 3 to be unpinned, pinned=%v", rt.pinned)
	}
	rt.maxRAMBytes = 200

	p2 := rt.Piece(info.Piece(2))
	p3 := rt.Piece(info.Piece(3))
	_, _ = p2.WriteAt(make([]byte, 50), 0) // older, still downloading
	_, _ = p3.WriteAt(make([]byte, 100), 0)
	_ = p3.MarkComplete()

	// Needs room: the complete piece 3 should go before the in-flight piece 2
	_, _ = rt.Piece(info.Piece(0)).WriteAt(make([]byte, 100), 0)

	if rt.pieces[2].data == nil {
		t.Error("in-flight piece 2 was evicted before complete piece 3")
	}
	if rt.pieces[3].data != nil {
		t.Error("expected complete piece 3 to be evicted")
	}
}

func TestRAMStorageOffsetBoundsAndPinCap(t *testing.T) {
	info := &metainfo.Info{
		PieceLength: 100,
		Pieces:      make([]byte, 20*2),
		Files: []metainfo.FileInfo{
			{Length: 200, Path: []string{"test.mp4"}},
		},
	}
	client := NewRAMStorageClient(1000, nil)
	ti, err := client.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatalf("failed to open torrent: %v", err)
	}
	defer ti.Close()

	rt := client.ActiveTorrent()
	p0 := rt.Piece(info.Piece(0))

	// Write valid chunk
	_, err = p0.WriteAt([]byte("hello"), 0)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}

	// Write with negative and out-of-bounds offset should not panic
	_, _ = p0.WriteAt([]byte("invalid"), -5)
	_, _ = p0.WriteAt([]byte("invalid"), 200)

	// Read with out-of-bounds offset should return EOF safely
	buf := make([]byte, 10)
	_, err = p0.ReadAt(buf, -1)
	if err == nil {
		t.Error("expected error/EOF for negative ReadAt offset")
	}
	_, err = p0.ReadAt(buf, 200)
	if err == nil {
		t.Error("expected error/EOF for out-of-bounds ReadAt offset")
	}
}
