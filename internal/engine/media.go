package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/anacrolix/torrent"
)

const (
	// defaultReadahead is used until mpv reports a duration a bitrate can be derived from.
	defaultReadahead = 32 << 20
	minReadahead     = 16 << 20
	maxReadahead     = 256 << 20
	// readaheadSeconds of playback are kept prioritized ahead of the read position.
	readaheadSeconds = 30

	minPrefetch = 32 << 20
	maxPrefetch = 384 << 20
	// prefetchSeconds of the next file's opening are downloaded ahead of time.
	prefetchSeconds = 120
)

// SetFileDuration records a file's playback duration as reported by mpv and derives its average
// bitrate from it. Files without a reported duration fall back to the most recent bitrate, since
// episodes in a pack are usually encoded alike.
func (e *Engine) SetFileDuration(fileIdx int, seconds float64) (int64, error) {
	f, err := e.file(fileIdx)
	if err != nil {
		return 0, err
	}
	if !(seconds > 0) || math.IsInf(seconds, 0) {
		return 0, fmt.Errorf("invalid duration %v", seconds)
	}
	bps := int64(float64(f.Length()) / seconds)
	if bps <= 0 {
		return 0, errors.New("file too small to derive a bitrate")
	}

	e.mediaMu.Lock()
	e.fileBitrates[fileIdx] = bps
	e.lastBitrate = bps
	e.mediaMu.Unlock()

	slog.Info("derived stream bitrate",
		"file", f.DisplayPath(),
		"kbps", bps*8/1000,
		"readahead_bytes", e.Readahead(fileIdx),
	)
	return bps, nil
}

// bitrate returns the file's bitrate in bytes per second, or 0 if none is known yet.
func (e *Engine) bitrate(fileIdx int) int64 {
	e.mediaMu.Lock()
	defer e.mediaMu.Unlock()
	if bps, ok := e.fileBitrates[fileIdx]; ok {
		return bps
	}
	return e.lastBitrate
}

// Readahead returns how many bytes to prioritize ahead of a reader of fileIdx: readaheadSeconds
// of playback at the file's bitrate. Readers call it with the torrent client locked, so it must
// not call back into the torrent.
func (e *Engine) Readahead(fileIdx int) int64 {
	bps := e.bitrate(fileIdx)
	if bps == 0 {
		return e.capToRAM(defaultReadahead)
	}
	return e.capToRAM(max(minReadahead, min(bps*readaheadSeconds, maxReadahead)))
}

// capToRAM limits a prioritized window to a quarter of the RAM ring buffer, so filling it can't
// evict the pieces that are about to be played.
func (e *Engine) capToRAM(n int64) int64 {
	if e.ramStorage == nil {
		return n
	}
	return min(n, int64(e.maxRAMMB)<<20/4)
}

// PrefetchFile starts downloading a file's container metadata and opening (prefetchSeconds of
// playback) so it starts instantly when playback reaches it. The opening gets normal priority,
// below the readahead of whatever is playing now. It returns the number of bytes queued.
func (e *Engine) PrefetchFile(fileIdx int) (int64, error) {
	f, err := e.file(fileIdx)
	if err != nil {
		return 0, err
	}
	e.PrioritizeFile(fileIdx)

	n := int64(minPrefetch)
	if bps := e.bitrate(fileIdx); bps > 0 {
		n = max(minPrefetch, min(bps*prefetchSeconds, maxPrefetch))
	}
	n = min(e.capToRAM(n), f.Length())
	if n <= 0 {
		return 0, nil
	}

	t := f.Torrent()
	pieceLen := t.Info().PieceLength
	first := int(f.Offset() / pieceLen)
	last := int((f.Offset() + n - 1) / pieceLen)
	// The header pieces already have high priority from PrioritizeFile
	for p := max(first, f.BeginPieceIndex()+headerPieces); p <= last; p++ {
		t.Piece(p).SetPriority(torrent.PiecePriorityNormal)
	}

	slog.Info("prefetching next file", "file", f.DisplayPath(), "bytes", n)
	return n, nil
}

// file returns the torrent's file at fileIdx once metadata is available.
func (e *Engine) file(fileIdx int) (*torrent.File, error) {
	t := e.Torrent()
	if t == nil || t.Info() == nil {
		return nil, errors.New("torrent not ready")
	}
	files := t.Files()
	if fileIdx < 0 || fileIdx >= len(files) {
		return nil, fmt.Errorf("file index %d out of range", fileIdx)
	}
	return files[fileIdx], nil
}
