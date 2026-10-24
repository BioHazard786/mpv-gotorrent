package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// Config defines configuration parameters for the torrent engine.
type Config struct {
	DataDir   string
	KeepFiles bool
	Port      int
}

// Engine wraps anacrolix/torrent client and manages active torrent streams.
type Engine struct {
	client    *torrent.Client
	torrent   *torrent.Torrent
	dataDir   string
	keepFiles bool

	mu             sync.RWMutex
	lastReadBytes  int64
	lastWriteBytes int64
	lastCheckTime  time.Time
	downloadSpeed  int64
	uploadSpeed    int64
}

// Stats encapsulates real-time streaming and swarm metrics.
type Stats struct {
	Name           string  `json:"name"`
	InfoHash       string  `json:"infohash"`
	TotalBytes     int64   `json:"total_bytes"`
	CompletedBytes int64   `json:"completed_bytes"`
	DownloadSpeed  int64   `json:"download_speed"` // bytes per second
	UploadSpeed    int64   `json:"upload_speed"`   // bytes per second
	Progress       float64 `json:"progress"`       // 0 - 100%
	Peers          int     `json:"peers"`
	ActivePeers    int     `json:"active_peers"`
	PieceCount     int     `json:"piece_count"`
	PiecesDone     int     `json:"pieces_done"`
}

// NewEngine initializes the BitTorrent client with optimized streaming defaults.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.DataDir == "" {
		tmp, err := os.MkdirTemp("", "mpv-gotorrent-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp dir: %w", err)
		}
		cfg.DataDir = tmp
	} else {
		if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create cache dir: %w", err)
		}
	}

	clientConfig := torrent.NewDefaultClientConfig()
	clientConfig.DataDir = cfg.DataDir
	clientConfig.ListenPort = cfg.Port
	clientConfig.NoUpload = false
	clientConfig.Seed = true
	// Enable standard DHT and peer exchange for fast swarm discovery
	clientConfig.DisableTCP = false
	clientConfig.DisableUTP = false

	slog.Debug("initializing torrent client", "cache_dir", cfg.DataDir, "port", cfg.Port)
	client, err := torrent.NewClient(clientConfig)
	if err != nil {
		if !cfg.KeepFiles {
			os.RemoveAll(cfg.DataDir)
		}
		return nil, fmt.Errorf("failed to initialize torrent client: %w", err)
	}

	eng := &Engine{
		client:        client,
		dataDir:       cfg.DataDir,
		keepFiles:     cfg.KeepFiles,
		lastCheckTime: time.Now(),
	}

	// Background ticker to calculate download / upload speeds
	go eng.speedMonitor()

	return eng, nil
}

// AddSource loads a magnet URI, local torrent file, remote torrent URL, or infohash.
func (e *Engine) AddSource(ctx context.Context, src string) (*torrent.Torrent, error) {
	src = strings.TrimSpace(src)
	var t *torrent.Torrent
	var err error

	if idx := strings.Index(src, "magnet:?"); idx != -1 {
		src = src[idx:]
		slog.Info("adding magnet URI")
		t, err = e.client.AddMagnet(src)
		if err != nil {
			return nil, fmt.Errorf("failed to add magnet: %w", err)
		}
	} else if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		slog.Info("fetching remote .torrent file", "url", src)
		t, err = e.addTorrentFromURL(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("failed to download torrent file from URL: %w", err)
		}
	} else if fileExists(src) {
		slog.Info("loading local .torrent file", "path", src)
		mi, parseErr := metainfo.LoadFromFile(src)
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse .torrent file %s: %w", src, parseErr)
		}
		t, err = e.client.AddTorrent(mi)
		if err != nil {
			return nil, fmt.Errorf("failed to add torrent from file: %w", err)
		}
	} else if isInfoHash(src) {
		magnet := "magnet:?xt=urn:btih:" + src
		slog.Info("resolving infohash to magnet", "infohash", src)
		t, err = e.client.AddMagnet(magnet)
		if err != nil {
			return nil, fmt.Errorf("failed to add infohash magnet: %w", err)
		}
	} else {
		return nil, fmt.Errorf("unrecognized source format: %s", src)
	}

	slog.Info("waiting for metadata from peers...")
	select {
	case <-t.GotInfo():
		slog.Info("metadata acquired",
			"name", t.Name(),
			"files", len(t.Files()),
			"total_bytes", t.Length(),
			"piece_count", t.NumPieces(),
		)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	e.mu.Lock()
	e.torrent = t
	e.mu.Unlock()

	// Prioritize header and footer pieces of each file for rapid demuxing
	e.prioritizeMetadataPieces(t)

	return t, nil
}

// prioritizeMetadataPieces prioritizes the initial and trailing pieces of files.
func (e *Engine) prioritizeMetadataPieces(t *torrent.Torrent) {
	for _, f := range t.Files() {
		begin := f.BeginPieceIndex()
		end := f.EndPieceIndex()
		if end <= begin {
			continue
		}

		// Prioritize first 2 pieces (or up to 4MB) for file header
		headEnd := begin + 3
		if headEnd > end {
			headEnd = end
		}
		for p := begin; p < headEnd; p++ {
			t.Piece(p).SetPriority(torrent.PiecePriorityHigh)
		}

		// Prioritize last piece for container index/moov atom (crucial for MP4/MKV)
		if end-1 >= begin {
			t.Piece(end - 1).SetPriority(torrent.PiecePriorityHigh)
		}

		slog.Debug("prioritized metadata pieces",
			"file", f.DisplayPath(),
			"head_pieces", fmt.Sprintf("[%d, %d)", begin, headEnd),
			"tail_piece", end-1,
		)
	}
}

// Torrent returns the active torrent.
func (e *Engine) Torrent() *torrent.Torrent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.torrent
}

// Stats returns the latest snapshot of torrent speed and peer metrics.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.torrent == nil || e.torrent.Info() == nil {
		return Stats{}
	}

	t := e.torrent
	totalBytes := t.Length()
	completedBytes := t.BytesCompleted()

	progress := 0.0
	if totalBytes > 0 {
		progress = float64(completedBytes) / float64(totalBytes) * 100.0
	}

	activePeers := 0
	for _, conn := range t.PeerConns() {
		if conn.Stats().RemotePieceCount > 0 {
			activePeers++
		}
	}

	piecesDone := 0
	for _, run := range t.PieceStateRuns() {
		if run.Complete {
			piecesDone += run.Length
		}
	}

	return Stats{
		Name:           t.Name(),
		InfoHash:       t.InfoHash().HexString(),
		TotalBytes:     totalBytes,
		CompletedBytes: completedBytes,
		DownloadSpeed:  e.downloadSpeed,
		UploadSpeed:    e.uploadSpeed,
		Progress:       progress,
		Peers:          len(t.PeerConns()),
		ActivePeers:    activePeers,
		PieceCount:     t.NumPieces(),
		PiecesDone:     piecesDone,
	}
}

func (e *Engine) speedMonitor() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		clStats := e.client.Stats()
		currentRead := clStats.BytesReadUsefulData.Int64()
		currentWrite := clStats.BytesWrittenData.Int64()
		now := time.Now()

		e.mu.Lock()
		elapsed := now.Sub(e.lastCheckTime).Seconds()
		if elapsed > 0 {
			e.downloadSpeed = int64(float64(currentRead-e.lastReadBytes) / elapsed)
			e.uploadSpeed = int64(float64(currentWrite-e.lastWriteBytes) / elapsed)
			if e.downloadSpeed < 0 {
				e.downloadSpeed = 0
			}
			if e.uploadSpeed < 0 {
				e.uploadSpeed = 0
			}
		}
		e.lastReadBytes = currentRead
		e.lastWriteBytes = currentWrite
		e.lastCheckTime = now
		e.mu.Unlock()
	}
}

// Close gracefully closes the client and cleans up disk cache if configured.
func (e *Engine) Close() error {
	slog.Info("closing torrent engine")
	var errMsgs []string
	if e.client != nil {
		errs := e.client.Close()
		for _, err := range errs {
			if err != nil {
				errMsgs = append(errMsgs, err.Error())
			}
		}
	}

	if !e.keepFiles && e.dataDir != "" {
		slog.Info("purging temporary cache directory", "path", e.dataDir)
		if err := os.RemoveAll(e.dataDir); err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("failed to remove cache dir %s: %v", e.dataDir, err))
		}
	}

	if len(errMsgs) > 0 {
		return errors.New(strings.Join(errMsgs, "; "))
	}
	return nil
}

func (e *Engine) addTorrentFromURL(ctx context.Context, url string) (*torrent.Torrent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp("", "torrent-*.torrent")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return nil, err
	}

	mi, err := metainfo.LoadFromFile(tmpFile.Name())
	if err != nil {
		return nil, err
	}
	return e.client.AddTorrent(mi)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func isInfoHash(s string) bool {
	if len(s) == 40 {
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	return false
}

// DataDir returns the path to the engine's data directory.
func (e *Engine) DataDir() string {
	return e.dataDir
}
