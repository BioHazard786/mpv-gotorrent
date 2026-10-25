package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	client             *torrent.Client
	torrent            *torrent.Torrent
	dataDir            string
	persistentCacheDir string
	keepFiles          bool
	trackerManager     *TrackerManager
	dhtManager         *DHTManager

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

	persistentDir := ""
	if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
		persistentDir = filepath.Join(userCache, "mpv-gotorrent")
	} else {
		persistentDir = cfg.DataDir
	}
	_ = os.MkdirAll(persistentDir, 0755)

	clientConfig := torrent.NewDefaultClientConfig()
	clientConfig.DataDir = cfg.DataDir
	clientConfig.ListenPort = cfg.Port
	clientConfig.NoUpload = false
	clientConfig.Seed = true
	// Enable standard DHT and peer exchange for fast swarm discovery
	clientConfig.DisableTCP = false
	clientConfig.DisableUTP = false

	slog.Debug("initializing torrent client", "cache_dir", cfg.DataDir, "persistent_dir", persistentDir, "port", cfg.Port)
	client, err := torrent.NewClient(clientConfig)
	if err != nil {
		if !cfg.KeepFiles {
			os.RemoveAll(cfg.DataDir)
		}
		return nil, fmt.Errorf("failed to initialize torrent client: %w", err)
	}

	tm := NewTrackerManager(persistentDir)
	dm := NewDHTManager(client, persistentDir)

	// Warm the DHT routing table immediately using cached nodes
	if count, err := dm.LoadNodes(); err == nil && count > 0 {
		slog.Info("warmed DHT routing table from cache", "nodes", count)
	}

	// Periodically sync DHT nodes (every 2 minutes)
	dm.StartPeriodicSync(2 * time.Minute)

	eng := &Engine{
		client:             client,
		dataDir:            cfg.DataDir,
		persistentCacheDir: persistentDir,
		keepFiles:          cfg.KeepFiles,
		trackerManager:     tm,
		dhtManager:         dm,
		lastCheckTime:      time.Now(),
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
	var isPrivate bool

	if idx := strings.Index(src, "magnet:?"); idx != -1 {
		src = src[idx:]

		// Attempt to parse magnet URI to check if we already have the .torrent cached
		if mag, parseErr := metainfo.ParseMagnetUri(src); parseErr == nil && !mag.InfoHash.IsZero() {
			cachedPath := e.getTorrentCachePath(mag.InfoHash.HexString())
			if fileExists(cachedPath) {
				slog.Info("loading cached .torrent metainfo for magnet", "path", cachedPath)
				if mi, err := metainfo.LoadFromFile(cachedPath); err == nil {
					t, err = e.client.AddTorrent(mi)
					if err != nil {
						slog.Warn("failed to add torrent from cache, falling back to network", "err", err)
						t = nil
					}
				}
			}
		}

		if t == nil {
			slog.Info("adding magnet URI")
			t, err = e.client.AddMagnet(src)
			if err != nil {
				return nil, fmt.Errorf("failed to add magnet: %w", err)
			}
		}

		e.injectPublicTrackers(t)
	} else if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		slog.Info("fetching remote .torrent file", "url", src)
		t, isPrivate, err = e.addTorrentFromURL(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("failed to download torrent file from URL: %w", err)
		}
		if !isPrivate {
			e.injectPublicTrackers(t)
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
		if info, parseErr := mi.UnmarshalInfo(); parseErr == nil && info.Private != nil && *info.Private {
			isPrivate = true
			slog.Info("torrent marked private, skipping public tracker injection")
		}
		if !isPrivate {
			e.injectPublicTrackers(t)
		}
	} else if isInfoHash(src) {
		cachedPath := e.getTorrentCachePath(src)
		if fileExists(cachedPath) {
			slog.Info("loading cached .torrent metainfo for instant playback", "path", cachedPath)
			mi, parseErr := metainfo.LoadFromFile(cachedPath)
			if parseErr == nil {
				t, err = e.client.AddTorrent(mi)
			} else {
				slog.Warn("failed to parse cached torrent", "err", parseErr)
			}
		}

		if t == nil {
			magnet := "magnet:?xt=urn:btih:" + src
			slog.Info("resolving infohash via BitTorrent DHT/swarm", "infohash", src)
			t, err = e.client.AddMagnet(magnet)
			if err != nil {
				return nil, fmt.Errorf("failed to add infohash magnet: %w", err)
			}
		}

		e.injectPublicTrackers(t)
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
		e.saveTorrentToCache(t)
		// Persist DHT nodes immediately to preserve fresh swarm peers
		if e.dhtManager != nil {
			go func() {
				_ = e.dhtManager.SaveNodes()
			}()
		}
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

// injectPublicTrackers announces the torrent to cached public trackers for rapid peer discovery.
func (e *Engine) injectPublicTrackers(t *torrent.Torrent) {
	if t == nil || e.trackerManager == nil {
		return
	}
	tiers := e.trackerManager.GetTrackers()
	if len(tiers) > 0 {
		t.AddTrackers(tiers)
		slog.Debug("injected public trackers for boost", "tiers_count", len(tiers))
	}
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

	if e.dhtManager != nil {
		if err := e.dhtManager.Close(); err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("dht shutdown error: %v", err))
		}
	}

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

func (e *Engine) addTorrentFromURL(ctx context.Context, url string) (*torrent.Torrent, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("HTTP error: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp("", "torrent-*.torrent")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return nil, false, err
	}

	mi, err := metainfo.LoadFromFile(tmpFile.Name())
	if err != nil {
		return nil, false, err
	}

	isPrivate := false
	if info, parseErr := mi.UnmarshalInfo(); parseErr == nil && info.Private != nil && *info.Private {
		isPrivate = true
	}

	t, err := e.client.AddTorrent(mi)
	return t, isPrivate, err
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func isInfoHash(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 40 || len(s) == 64 {
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	if len(s) == 32 {
		for _, c := range s {
			if !((c >= '2' && c <= '7') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
				return false
			}
		}
		return true
	}
	return false
}

func (e *Engine) getTorrentCachePath(infoHash string) string {
	base := e.persistentCacheDir
	if base == "" {
		base = e.dataDir
		if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
			base = filepath.Join(userCache, "mpv-gotorrent")
		}
	}
	dir := filepath.Join(base, "torrents")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, strings.ToLower(infoHash)+".torrent")
}

func (e *Engine) saveTorrentToCache(t *torrent.Torrent) {
	if t == nil {
		return
	}
	hash := t.InfoHash().HexString()
	path := e.getTorrentCachePath(hash)
	f, err := os.Create(path)
	if err != nil {
		slog.Debug("failed to create torrent cache file", "path", path, "err", err)
		return
	}
	defer f.Close()
	mi := t.Metainfo()
	if err := mi.Write(f); err != nil {
		slog.Debug("failed to write torrent metainfo to cache", "path", path, "err", err)
	} else {
		slog.Info("cached torrent metainfo for instant reopening", "path", path)
	}
}

// DataDir returns the path to the engine's data directory.
func (e *Engine) DataDir() string {
	return e.dataDir
}

