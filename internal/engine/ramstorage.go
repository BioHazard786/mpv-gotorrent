package engine

import (
	"container/list"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sort"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// RAMStorageClient implements storage.ClientImpl for in-memory streaming with zero SSD wear.
type RAMStorageClient struct {
	maxRAMBytes int64
	active      *RAMTorrentStorage
	onEvict     func(pieceIdx int)
	mu          sync.Mutex
}

// NewRAMStorageClient creates a new RAM-only storage backend with the given maximum memory limit.
func NewRAMStorageClient(maxRAMBytes int64, onEvict func(pieceIdx int)) *RAMStorageClient {
	return &RAMStorageClient{
		maxRAMBytes: maxRAMBytes,
		onEvict:     onEvict,
	}
}

// ActiveTorrent returns the currently loaded in-memory torrent storage.
func (c *RAMStorageClient) ActiveTorrent() *RAMTorrentStorage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// OpenTorrent initializes in-memory piece tables and calculates header/footer pinned pieces.
func (c *RAMStorageClient) OpenTorrent(
	_ context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ts := newRAMTorrentStorage(info, infoHash, c.maxRAMBytes, c.onEvict)
	c.active = ts

	return storage.TorrentImpl{
		Piece: ts.Piece,
		Close: ts.Close,
	}, nil
}

// RAMTorrentStorage manages piece memory buffers and LRU eviction.
type RAMTorrentStorage struct {
	info        *metainfo.Info
	infoHash    metainfo.Hash
	maxRAMBytes int64
	pieceLength int64
	numPieces   int

	pieces   []*ramPiece
	pinned   map[int]bool
	lruList  *list.List
	lruNodes map[int]*list.Element

	usedRAMBytes int64
	onEvict      func(pieceIdx int)

	// mu guards the LRU, usedRAMBytes and allocation/eviction of piece buffers. Lock order is
	// mu before a piece's own mutex.
	mu sync.Mutex
}

type ramPiece struct {
	index    int
	length   int64
	offset   int64
	data     []byte
	complete bool
	mu       sync.RWMutex
}

func newRAMTorrentStorage(
	info *metainfo.Info,
	infoHash metainfo.Hash,
	maxRAMBytes int64,
	onEvict func(pieceIdx int),
) *RAMTorrentStorage {
	numPieces := info.NumPieces()
	pieceLength := info.PieceLength

	ts := &RAMTorrentStorage{
		info:        info,
		infoHash:    infoHash,
		maxRAMBytes: maxRAMBytes,
		pieceLength: pieceLength,
		numPieces:   numPieces,
		pieces:      make([]*ramPiece, numPieces),
		pinned:      make(map[int]bool),
		lruList:     list.New(),
		lruNodes:    make(map[int]*list.Element),
		onEvict:     onEvict,
	}

	for i := 0; i < numPieces; i++ {
		pLen := pieceLength
		if i == numPieces-1 {
			pLen = info.Piece(i).Length()
		}
		ts.pieces[i] = &ramPiece{
			index:  i,
			length: pLen,
			offset: info.Piece(i).Offset(),
		}
	}

	// Pin the first and last 2 pieces of each file (headers, cues, moov atoms) to prevent
	// seek/metadata thrashing. Largest files are pinned first, and pinning stops at half the
	// RAM budget so a season pack can't pin the whole cache.
	files := slices.Clone(info.UpvertedFiles())
	sort.SliceStable(files, func(i, j int) bool { return files[i].Length > files[j].Length })
	pinBudget := maxRAMBytes / 2
	var pinnedBytes int64
	for _, fi := range files {
		begin := fi.BeginPieceIndex(pieceLength)
		end := min(fi.EndPieceIndex(pieceLength), numPieces)

		filePins := make(map[int]bool)
		for p := begin; p < begin+2 && p < end; p++ {
			filePins[p] = true
		}
		for p := end - 1; p >= end-2 && p >= begin && p >= 0; p-- {
			filePins[p] = true
		}

		var cost int64
		for p := range filePins {
			if !ts.pinned[p] {
				cost += ts.pieces[p].length
			}
		}
		if pinnedBytes+cost > pinBudget {
			break
		}
		for p := range filePins {
			ts.pinned[p] = true
		}
		pinnedBytes += cost
	}

	slog.Debug("initialized RAM storage",
		"total_pieces", numPieces,
		"pinned_pieces", len(ts.pinned),
		"max_ram_bytes", maxRAMBytes,
	)

	return ts
}

// Piece returns the piece storage implementation for a given piece index.
func (ts *RAMTorrentStorage) Piece(p metainfo.Piece) storage.PieceImpl {
	idx := p.Index()
	return ramPieceImpl{
		ts: ts,
		rp: ts.pieces[idx],
	}
}

// UsedRAMBytes returns the current memory consumed by torrent pieces.
func (ts *RAMTorrentStorage) UsedRAMBytes() int64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.usedRAMBytes
}

// Close releases all in-memory buffers.
func (ts *RAMTorrentStorage) Close() error {
	ts.mu.Lock()
	for _, p := range ts.pieces {
		p.mu.Lock()
		p.data = nil
		p.complete = false
		p.mu.Unlock()
	}
	ts.lruList.Init()
	clear(ts.lruNodes)
	ts.usedRAMBytes = 0
	ts.mu.Unlock()
	return nil
}

// writePiece copies b into the piece buffer, allocating it (and evicting others) if needed.
// ts.mu is held throughout so an eviction can't free the buffer mid-write.
func (ts *RAMTorrentStorage) writePiece(rp *ramPiece, b []byte, off int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if rp.data == nil {
		for ts.usedRAMBytes+rp.length > ts.maxRAMBytes {
			if !ts.evictOneLocked() {
				// Everything left is pinned; go over budget rather than spin.
				slog.Debug("RAM budget exceeded, no evictable pieces", "used_bytes", ts.usedRAMBytes)
				break
			}
		}
		rp.mu.Lock()
		rp.data = make([]byte, rp.length)
		rp.mu.Unlock()
		ts.usedRAMBytes += rp.length
		ts.lruNodes[rp.index] = ts.lruList.PushFront(rp.index)
	} else if el, ok := ts.lruNodes[rp.index]; ok {
		ts.lruList.MoveToFront(el)
	}

	rp.mu.Lock()
	if off >= 0 && off < int64(len(rp.data)) {
		copy(rp.data[off:], b)
	}
	rp.mu.Unlock()
}

// evictOneLocked frees the least recently used unpinned piece, preferring complete pieces so
// in-flight downloads aren't thrown away. It reports false if nothing could be evicted.
func (ts *RAMTorrentStorage) evictOneLocked() bool {
	var victim *list.Element
	for el := ts.lruList.Back(); el != nil; el = el.Prev() {
		idx := el.Value.(int)
		if ts.pinned[idx] {
			continue
		}
		p := ts.pieces[idx]
		p.mu.RLock()
		complete := p.complete
		p.mu.RUnlock()
		if complete {
			victim = el
			break
		}
		if victim == nil {
			victim = el
		}
	}
	if victim == nil {
		return false
	}

	idx := victim.Value.(int)
	p := ts.pieces[idx]
	p.mu.Lock()
	p.data = nil
	p.complete = false
	p.mu.Unlock()
	ts.usedRAMBytes -= p.length

	ts.lruList.Remove(victim)
	delete(ts.lruNodes, idx)

	if ts.onEvict != nil {
		// Run eviction notification asynchronously to prevent lock deadlock
		go ts.onEvict(idx)
	}
	return true
}

func (ts *RAMTorrentStorage) touchPiece(idx int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if el, ok := ts.lruNodes[idx]; ok {
		ts.lruList.MoveToFront(el)
	}
}

type ramPieceImpl struct {
	ts *RAMTorrentStorage
	rp *ramPiece
}

func (m ramPieceImpl) ReadAt(b []byte, off int64) (int, error) {
	m.rp.mu.RLock()
	if m.rp.data == nil {
		m.rp.mu.RUnlock()
		return 0, errors.New("piece data not present in storage")
	}
	if off < 0 || off >= int64(len(m.rp.data)) {
		m.rp.mu.RUnlock()
		return 0, io.EOF
	}
	n := copy(b, m.rp.data[off:])
	m.rp.mu.RUnlock()

	m.ts.touchPiece(m.rp.index)
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (m ramPieceImpl) WriteAt(b []byte, off int64) (int, error) {
	m.ts.writePiece(m.rp, b, off)
	return len(b), nil
}

func (m ramPieceImpl) MarkComplete() error {
	m.rp.mu.Lock()
	m.rp.complete = true
	m.rp.mu.Unlock()
	return nil
}

func (m ramPieceImpl) MarkNotComplete() error {
	m.rp.mu.Lock()
	m.rp.complete = false
	m.rp.mu.Unlock()
	return nil
}

func (m ramPieceImpl) Completion() storage.Completion {
	m.rp.mu.RLock()
	defer m.rp.mu.RUnlock()
	return storage.Completion{
		Complete: m.rp.complete,
		Ok:       true,
	}
}
