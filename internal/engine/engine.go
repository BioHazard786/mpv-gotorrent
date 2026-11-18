package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

const (
	// heartbeatFile is touched periodically so other instances can tell a session dir is still in use.
	heartbeatFile     = ".alive"
	heartbeatInterval = time.Minute
	staleSessionAge   = 5 * time.Minute

	// headerPieces at the start of each file are fetched first for container headers.
	headerPieces = 3
)

// Config defines configuration parameters for the torrent engine.
type Config struct {
	DataDir   string
	KeepFiles bool
	Port      int
	MaxRAMMB  int
	KeepDir   string
	KeepScope string

	// MaxCachedFiles caps how many streamed files stay on disk; older ones are deleted as new
	// ones start (0 = keep everything until exit). Ignored in RAM mode and with KeepFiles.
	MaxCachedFiles int

	// MaxUploadKBps and MaxDownloadKBps cap transfer rates in KiB/s (0 = unlimited).
	MaxUploadKBps   int
	MaxDownloadKBps int

	// SeedRatio caps uploading at this multiple of what was downloaded this session (0 = no cap).
	SeedRatio float64
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
	ramStorage         *RAMStorageClient
	maxRAMMB           int
	defaultKeepDir     string
	defaultKeepScope   string

	// Keep File state: one job per Ctrl+K, so several episodes of a pack can be saved
	keepMu sync.RWMutex
	keeps  []*keepJob

	prioritizedMu sync.Mutex
	prioritized   map[int]bool

	maxCachedFiles int
	seedRatio      float64
	uploadPaused   bool           // guarded by mu
	streamed       []int          // streamed file indexes, most recent first
	freeing        sync.WaitGroup // background deletions of files that left the window

	mediaMu      sync.Mutex
	fileBitrates map[int]int64 // bytes per second, derived from durations mpv reports
	lastBitrate  int64

	done      chan struct{}
	closeOnce sync.Once

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
	Seeders        int     `json:"seeders"`
	UploadPaused   bool    `json:"upload_paused,omitempty"` // seed ratio reached
	StorageMode    string  `json:"storage_mode"`            // "ram" or "disk"
	RAMUsedBytes   int64   `json:"ram_used_bytes,omitempty"`
	RAMMaxBytes    int64   `json:"ram_max_bytes,omitempty"`
	KeepActive     bool    `json:"keep_active"`
	KeepPath       string  `json:"keep_path,omitempty"` // destination of the latest keep
	KeepProgress   float64 `json:"keep_progress,omitempty"`
	KeepCount      int     `json:"keep_count,omitempty"`
}

// NewEngine initializes the BitTorrent client with optimized streaming defaults.
func NewEngine(cfg Config) (*Engine, error) {
	persistentDir := filepath.Join(os.TempDir(), "mpv-gotorrent")
	if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
		persistentDir = filepath.Join(userCache, "mpv-gotorrent")
	}
	_ = os.MkdirAll(persistentDir, 0755)

	// With keep-files the cache dir is used as-is so downloads persist across sessions.
	// Otherwise each session gets its own subdirectory, so purging it on exit never touches
	// anything else in a user-supplied cache dir. The default lives under the user cache dir
	// rather than /tmp, which is RAM-backed tmpfs on many distros.
	if cfg.KeepFiles {
		if cfg.DataDir == "" {
			cfg.DataDir = filepath.Join(persistentDir, "downloads")
		}
		if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create cache dir: %w", err)
		}
	} else {
		sessionsDir := cfg.DataDir
		if sessionsDir == "" {
			sessionsDir = filepath.Join(persistentDir, "sessions")
		}
		if err := os.MkdirAll(sessionsDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create cache dir: %w", err)
		}
		tmp, err := os.MkdirTemp(sessionsDir, "mpv-gotorrent-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create session dir: %w", err)
		}
		touchHeartbeat(tmp)
		cfg.DataDir = tmp
		go cleanStaleSessionDirs(tmp, sessionsDir, os.TempDir())
	}

	clientConfig := torrent.NewDefaultClientConfig()
	clientConfig.DataDir = cfg.DataDir
	clientConfig.ListenPort = cfg.Port
	clientConfig.NoUpload = false
	clientConfig.Seed = true
	// Enable standard DHT and peer exchange for fast swarm discovery
	clientConfig.DisableTCP = false
	clientConfig.DisableUTP = false

	// Bursts are left at 0 so anacrolix sizes them to its request size
	if cfg.MaxUploadKBps > 0 {
		clientConfig.UploadRateLimiter = rate.NewLimiter(rate.Limit(cfg.MaxUploadKBps*1024), 0)
	}
	if cfg.MaxDownloadKBps > 0 {
		clientConfig.DownloadRateLimiter = rate.NewLimiter(rate.Limit(cfg.MaxDownloadKBps*1024), 0)
	}

	var ramStorage *RAMStorageClient
	if cfg.MaxRAMMB > 0 {
		maxBytes := int64(cfg.MaxRAMMB) * 1024 * 1024
		ramStorage = NewRAMStorageClient(maxBytes, nil)
		clientConfig.DefaultStorage = ramStorage
		clientConfig.NoUpload = true // In RAM mode, do not seed evicted pieces to avoid peer choking
		slog.Info("enabled RAM-only ring buffer mode (Zero SSD Wear)", "max_ram_mb", cfg.MaxRAMMB)
	}

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

	// A window of 1 would delete the playing file when mpv opens the next one early
	// (--prefetch-playlist), so 2 is the smallest that is safe.
	if cfg.MaxCachedFiles == 1 {
		cfg.MaxCachedFiles = 2
	}

	eng := &Engine{
		client:             client,
		dataDir:            cfg.DataDir,
		persistentCacheDir: persistentDir,
		keepFiles:          cfg.KeepFiles,
		trackerManager:     tm,
		dhtManager:         dm,
		ramStorage:         ramStorage,
		maxRAMMB:           cfg.MaxRAMMB,
		defaultKeepDir:     cfg.KeepDir,
		defaultKeepScope:   cfg.KeepScope,
		maxCachedFiles:     cfg.MaxCachedFiles,
		seedRatio:          cfg.SeedRatio,
		prioritized:        make(map[int]bool),
		fileBitrates:       make(map[int]int64),
		done:               make(chan struct{}),
		lastCheckTime:      time.Now(),
	}

	if ramStorage != nil {
		ramStorage.onEvict = eng.onPieceEvicted
	}

	// Background ticker to calculate download / upload speeds
	go eng.speedMonitor()
	if !cfg.KeepFiles {
		go eng.heartbeat()
	}

	return eng, nil
}

// AddSource loads a magnet URI, local torrent file, remote torrent URL, or infohash.
func (e *Engine) AddSource(ctx context.Context, src string) (*torrent.Torrent, error) {
	src = strings.TrimSpace(src)
	if !fileExists(src) && isInfoHash(src) {
		slog.Info("resolving infohash via BitTorrent DHT/swarm", "infohash", src)
		src = "magnet:?xt=urn:btih:" + src
	}

	var t *torrent.Torrent
	var err error

	if idx := strings.Index(src, "magnet:?"); idx != -1 {
		t, err = e.addMagnet(src[idx:])
	} else if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		slog.Info("fetching remote .torrent file", "url", src)
		mi, magnet, fetchErr := e.fetchTorrentURL(ctx, src)
		if fetchErr != nil {
			return nil, fmt.Errorf("failed to download torrent file from URL: %w", fetchErr)
		}
		if magnet != "" {
			slog.Info("torrent URL redirected to a magnet link")
			t, err = e.addMagnet(magnet)
		} else {
			t, err = e.addMetaInfo(mi)
		}
	} else if fileExists(src) {
		slog.Info("loading local .torrent file", "path", src)
		mi, parseErr := metainfo.LoadFromFile(src)
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse .torrent file %s: %w", src, parseErr)
		}
		t, err = e.addMetaInfo(mi)
	} else {
		return nil, fmt.Errorf("unrecognized source format: %s", src)
	}
	if err != nil {
		return nil, err
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

	return t, nil
}

// addMagnet adds a magnet URI, using cached metainfo when this infohash has been seen before.
func (e *Engine) addMagnet(uri string) (*torrent.Torrent, error) {
	if mag, err := metainfo.ParseMagnetUri(uri); err == nil && !mag.InfoHash.IsZero() {
		cachedPath := e.getTorrentCachePath(mag.InfoHash.HexString())
		if fileExists(cachedPath) {
			slog.Info("loading cached .torrent metainfo for magnet", "path", cachedPath)
			if mi, err := metainfo.LoadFromFile(cachedPath); err == nil {
				t, err := e.addMetaInfo(mi)
				if err == nil {
					if len(mag.Trackers) > 0 {
						t.AddTrackers([][]string{mag.Trackers})
					}
					return t, nil
				}
				slog.Warn("failed to add torrent from cache, falling back to network", "err", err)
			}
		}
	}

	slog.Info("adding magnet URI")
	t, err := e.client.AddMagnet(uri)
	if err != nil {
		return nil, fmt.Errorf("failed to add magnet: %w", err)
	}
	e.injectPublicTrackers(t)
	return t, nil
}

// addMetaInfo adds parsed torrent metainfo, skipping public tracker injection for private torrents.
func (e *Engine) addMetaInfo(mi *metainfo.MetaInfo) (*torrent.Torrent, error) {
	t, err := e.client.AddTorrent(mi)
	if err != nil {
		return nil, fmt.Errorf("failed to add torrent: %w", err)
	}
	if info, parseErr := mi.UnmarshalInfo(); parseErr == nil && info.Private != nil && *info.Private {
		slog.Info("torrent marked private, skipping public tracker injection")
		return t, nil
	}
	e.injectPublicTrackers(t)
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

// PrioritizeFile raises the priority of a file's leading and trailing pieces (container headers,
// MKV cues, MP4 moov atoms) so demuxing and seeking start quickly. It runs once per file, when the
// file is first streamed, so multi-file torrents don't fetch every file's head and tail up front.
func (e *Engine) PrioritizeFile(fileIdx int) {
	t := e.Torrent()
	if t == nil || t.Info() == nil {
		return
	}
	files := t.Files()
	if fileIdx < 0 || fileIdx >= len(files) {
		return
	}

	e.prioritizedMu.Lock()
	if e.prioritized[fileIdx] {
		e.prioritizedMu.Unlock()
		return
	}
	e.prioritized[fileIdx] = true
	e.prioritizedMu.Unlock()

	f := files[fileIdx]
	begin := f.BeginPieceIndex()
	end := f.EndPieceIndex()
	if end <= begin {
		return
	}

	headEnd := begin + headerPieces
	if headEnd > end {
		headEnd = end
	}
	for p := begin; p < headEnd; p++ {
		t.Piece(p).SetPriority(torrent.PiecePriorityHigh)
	}

	// Prioritize last piece for container index/moov atom (crucial for MP4/MKV)
	t.Piece(end - 1).SetPriority(torrent.PiecePriorityHigh)

	slog.Debug("prioritized metadata pieces",
		"file", f.DisplayPath(),
		"head_pieces", fmt.Sprintf("[%d, %d)", begin, headEnd),
		"tail_piece", end-1,
	)
}

// NoteStreamed records that fileIdx is being played and, in disk mode, deletes the cached data of
// files that fell out of the MaxCachedFiles window so a long pack doesn't fill the disk.
func (e *Engine) NoteStreamed(fileIdx int) {
	if e.maxCachedFiles <= 0 || e.ramStorage != nil || e.keepFiles {
		return
	}

	e.prioritizedMu.Lock()
	if len(e.streamed) > 0 && e.streamed[0] == fileIdx {
		e.prioritizedMu.Unlock()
		return
	}
	order := []int{fileIdx}
	for _, i := range e.streamed {
		if i != fileIdx {
			order = append(order, i)
		}
	}
	var stale []int
	if len(order) > e.maxCachedFiles {
		stale = order[e.maxCachedFiles:]
		order = order[:e.maxCachedFiles]
	}
	e.streamed = order
	for _, i := range stale {
		delete(e.prioritized, i) // so replaying it re-prioritizes its headers
	}
	e.prioritizedMu.Unlock()

	if len(stale) == 0 {
		return
	}
	t := e.Torrent()
	if t == nil || t.Info() == nil {
		return
	}
	kept := map[*torrent.File]bool{}
	e.keepMu.RLock()
	for _, j := range e.keeps {
		if j.file == nil {
			e.keepMu.RUnlock()
			return // the whole pack is being saved
		}
		kept[j.file] = true
	}
	e.keepMu.RUnlock()

	// Marking pieces incomplete is one database write each (seconds for a large episode), so it
	// runs in the background instead of delaying the new episode's first response.
	e.freeing.Add(1)
	go func() {
		defer e.freeing.Done()
		for _, i := range stale {
			if f := t.Files()[i]; !kept[f] {
				e.freeFile(t, f)
			}
		}
	}()
}

// freeFile drops a file's pieces and deletes its data from the session cache.
func (e *Engine) freeFile(t *torrent.Torrent, f *torrent.File) {
	for p := f.BeginPieceIndex(); p < f.EndPieceIndex(); p++ {
		select {
		case <-e.done:
			return // engine closing; the whole cache dir is purged anyway
		default:
		}
		piece := t.Piece(p)
		piece.SetPriority(torrent.PiecePriorityNone)
		// Pieces shared with a neighbouring file are re-fetched if that file is replayed
		_ = piece.Storage().MarkNotComplete()
		piece.UpdateCompletion()
	}
	// Incomplete files are stored as <name>.part until their last piece lands
	path := e.cachedFilePath(t, f)
	for _, p := range []string{path, path + ".part"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			slog.Debug("failed to delete old cached file", "file", p, "err", err)
		}
	}
	slog.Info("freed cached file", "file", f.DisplayPath())
}

// Torrent returns the active torrent.
func (e *Engine) Torrent() *torrent.Torrent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.torrent
}

// onPieceEvicted notifies anacrolix/torrent when an unpinned piece is evicted from RAM. Any
// explicit priority (prefetch, headers) is dropped so the piece isn't fetched straight back into
// the full buffer; readers raise it again when playback reaches it.
func (e *Engine) onPieceEvicted(pieceIdx int) {
	e.mu.RLock()
	t := e.torrent
	e.mu.RUnlock()
	if t != nil {
		p := t.Piece(pieceIdx)
		if p != nil {
			p.SetPriority(torrent.PiecePriorityNone)
			p.UpdateCompletion()
		}
	}
}

// Stats returns the latest snapshot of torrent speed and peer metrics.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	t := e.torrent
	downloadSpeed := e.downloadSpeed
	uploadSpeed := e.uploadSpeed
	uploadPaused := e.uploadPaused
	e.mu.RUnlock()

	if t == nil || t.Info() == nil {
		return Stats{}
	}

	totalBytes := t.Length()
	completedBytes := t.BytesCompleted()

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

	storageMode := "disk"
	ramUsed := int64(0)
	ramMax := int64(0)
	if e.ramStorage != nil {
		storageMode = "ram"
		if at := e.ramStorage.ActiveTorrent(); at != nil {
			ramUsed = at.UsedRAMBytes()
		}
		ramMax = int64(e.maxRAMMB) * 1024 * 1024
	}

	var keepPath string
	var keepDone, keepTotal int64
	e.keepMu.RLock()
	keepCount := len(e.keeps)
	for _, j := range e.keeps {
		done, total := keepBytes(t, j.file)
		keepDone += done
		keepTotal += total
		keepPath = j.path
	}
	e.keepMu.RUnlock()

	return Stats{
		Name:           t.Name(),
		InfoHash:       t.InfoHash().HexString(),
		TotalBytes:     totalBytes,
		CompletedBytes: completedBytes,
		DownloadSpeed:  downloadSpeed,
		UploadSpeed:    uploadSpeed,
		Progress:       percent(completedBytes, totalBytes),
		Peers:          len(t.PeerConns()),
		ActivePeers:    activePeers,
		Seeders:        t.Stats().ConnectedSeeders,
		UploadPaused:   uploadPaused,
		PieceCount:     t.NumPieces(),
		PiecesDone:     piecesDone,
		StorageMode:    storageMode,
		RAMUsedBytes:   ramUsed,
		RAMMaxBytes:    ramMax,
		KeepActive:     keepCount > 0,
		KeepPath:       keepPath,
		KeepProgress:   percent(keepDone, keepTotal),
		KeepCount:      keepCount,
	}
}

func (e *Engine) speedMonitor() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.done:
			return
		case <-ticker.C:
		}

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

		e.enforceSeedRatio(currentRead, currentWrite)
	}
}

// enforceSeedRatio pauses uploading while this session has uploaded at least seedRatio times what
// it downloaded, and resumes it as more is downloaded, so upload stays capped at that multiple.
func (e *Engine) enforceSeedRatio(downloaded, uploaded int64) {
	t := e.Torrent()
	if e.seedRatio <= 0 || t == nil {
		return
	}
	over := float64(uploaded) >= e.seedRatio*float64(downloaded)

	e.mu.Lock()
	changed := over != e.uploadPaused
	e.uploadPaused = over
	e.mu.Unlock()
	if !changed {
		return
	}
	if over {
		t.DisallowDataUpload()
		slog.Info("seed ratio reached, pausing upload", "ratio", e.seedRatio, "uploaded", uploaded, "downloaded", downloaded)
	} else {
		t.AllowDataUpload()
		slog.Debug("below seed ratio, resuming upload")
	}
}

// SwarmCounts reports connected peers and seeders. It also works while metadata is still being
// fetched, before Torrent() is set, so the player can show whether a swarm exists at all.
func (e *Engine) SwarmCounts() (peers, seeders int) {
	for _, t := range e.client.Torrents() {
		st := t.Stats()
		peers += st.ActivePeers
		seeders += st.ConnectedSeeders
	}
	return peers, seeders
}

// DiskSpaceWarning explains when the cache drive can't hold the largest file still to download,
// so the user can switch to RAM mode before playback stalls on a full disk. Empty when there is
// room, in RAM mode, or when free space can't be read on this platform.
func (e *Engine) DiskSpaceWarning() string {
	t := e.Torrent()
	if e.ramStorage != nil || t == nil || t.Info() == nil {
		return ""
	}
	var need int64
	for _, f := range t.Files() {
		need = max(need, f.Length()-f.BytesCompleted())
	}
	free, ok := freeDiskBytes(e.dataDir)
	if !ok || free >= uint64(need) {
		return ""
	}
	return fmt.Sprintf("Low disk space: %s free, but the largest file needs %s. Set max_ram_mb to stream from RAM.",
		formatGB(int64(free)), formatGB(need))
}

func formatGB(n int64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// heartbeat keeps the session dir's heartbeat fresh so cleanStaleSessionDirs leaves it alone.
func (e *Engine) heartbeat() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.done:
			return
		case <-ticker.C:
			touchHeartbeat(e.dataDir)
		}
	}
}

// KeepStatus reports current preservation progress and target destination.
type KeepStatus struct {
	Status         string  `json:"status"` // "keeping", "already_keeping", "completed", "error"
	Name           string  `json:"name"`
	DestPath       string  `json:"dest_path"`
	Progress       float64 `json:"progress"`
	CompletedBytes int64   `json:"completed_bytes"`
	TotalBytes     int64   `json:"total_bytes"`
	Scope          string  `json:"scope"`
	Message        string  `json:"message,omitempty"`
}

// keepJob is one keep request: a single file, or the whole torrent when file is nil.
type keepJob struct {
	file      *torrent.File
	name      string
	path      string
	completed bool          // guarded by keepMu
	done      chan struct{} // closed once the export finishes
}

func (e *Engine) IsKeepActive() bool {
	e.keepMu.RLock()
	defer e.keepMu.RUnlock()
	return len(e.keeps) > 0
}

// IsKeepCompleted reports whether every keep has finished exporting.
func (e *Engine) IsKeepCompleted() bool {
	e.keepMu.RLock()
	defer e.keepMu.RUnlock()
	for _, j := range e.keeps {
		if !j.completed {
			return false
		}
	}
	return true
}

// KeepName lists the keeps still downloading, for the background-download notification.
func (e *Engine) KeepName() string {
	e.keepMu.RLock()
	defer e.keepMu.RUnlock()
	var names []string
	for _, j := range e.keeps {
		if !j.completed {
			names = append(names, j.name)
		}
	}
	return strings.Join(names, ", ")
}

// WaitKeepComplete returns a channel closed once every keep requested so far has finished.
func (e *Engine) WaitKeepComplete() <-chan struct{} {
	e.keepMu.RLock()
	jobs := slices.Clone(e.keeps)
	e.keepMu.RUnlock()
	ch := make(chan struct{})
	go func() {
		for _, j := range jobs {
			<-j.done
		}
		close(ch)
	}()
	return ch
}

// keepBytes reports completed and total bytes of a kept file, or of the whole torrent for nil.
func keepBytes(t *torrent.Torrent, f *torrent.File) (done, total int64) {
	if f == nil {
		return t.BytesCompleted(), t.Length()
	}
	return f.BytesCompleted(), f.Length()
}

func (j *keepJob) status(t *torrent.Torrent, status string) KeepStatus {
	scope := "current"
	if j.file == nil {
		scope = "all"
	}
	done, total := keepBytes(t, j.file)
	return KeepStatus{
		Status:         status,
		Name:           j.name,
		DestPath:       j.path,
		Progress:       percent(done, total),
		TotalBytes:     total,
		CompletedBytes: done,
		Scope:          scope,
	}
}

func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(done) / float64(total) * 100.0
}

// KeepCurrent initiates saving the stream to permanent library disk storage.
// It is only available in disk mode; RAM mode never writes torrent data to disk.
func (e *Engine) KeepCurrent(scope, targetDir string, fileIdx int) (KeepStatus, error) {
	t := e.Torrent()
	if t == nil || t.Info() == nil {
		return KeepStatus{Status: "error", Message: "torrent metadata not ready"}, errors.New("torrent not ready")
	}

	if e.ramStorage != nil {
		return KeepStatus{Status: "error", Message: "Keep needs disk mode (set max_ram_mb=0)"}, errors.New("keep is not supported in RAM mode")
	}

	if scope == "" {
		scope = e.defaultKeepScope
		if scope == "" {
			scope = "current"
		}
	}

	files := t.Files()
	var targetFile *torrent.File // stays nil for scope "all"
	if scope != "all" {
		if fileIdx >= 0 && fileIdx < len(files) {
			targetFile = files[fileIdx]
		} else {
			var maxLen int64 = -1
			for _, f := range files {
				if f.Length() > maxLen {
					maxLen = f.Length()
					targetFile = f
				}
			}
		}
		if targetFile == nil {
			return KeepStatus{Status: "error", Message: "no files in torrent"}, errors.New("no files in torrent")
		}
	}

	e.keepMu.Lock()
	defer e.keepMu.Unlock()

	// Already covered by an earlier keep of this file or of the whole torrent
	for _, j := range e.keeps {
		if j.file == nil || j.file == targetFile {
			status := "already_keeping"
			if j.completed {
				status = "completed"
			}
			return j.status(t, status), nil
		}
	}

	// Resolve destination directory (defaults to configured keepDir, ~/Videos, or ~/Downloads)
	if targetDir == "" {
		targetDir = e.defaultKeepDir
	}
	if targetDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			videosDir := filepath.Join(home, "Videos")
			if fi, err := os.Stat(videosDir); err == nil && fi.IsDir() {
				targetDir = videosDir
			} else {
				targetDir = filepath.Join(home, "Downloads")
			}
		} else {
			targetDir = "."
		}
	} else if strings.HasPrefix(targetDir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			targetDir = filepath.Join(home, targetDir[1:])
		}
	}
	_ = os.MkdirAll(targetDir, 0755)

	name := t.Name()
	if targetFile != nil {
		name = filepath.Base(targetFile.DisplayPath())
	}
	job := &keepJob{file: targetFile, name: name, path: filepath.Join(targetDir, name), done: make(chan struct{})}
	e.keeps = append(e.keeps, job)

	// Switch from read-ahead to full background download
	if targetFile == nil {
		t.DownloadAll()
	} else {
		targetFile.Download()
	}

	// Start background watcher to detect when download hits 100%
	go e.watchKeepCompletion(t, job)

	slog.Info("keep file activated", "name", name, "dest_path", job.path, "scope", scope)
	return job.status(t, "keeping"), nil
}

func (e *Engine) watchKeepCompletion(t *torrent.Torrent, job *keepJob) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.done:
			return
		case <-ticker.C:
		}

		if done, total := keepBytes(t, job.file); done >= total {
			e.finalizeKeep(t, job)
			return
		}
	}
}

// finalizeKeep exports the completed files from the session cache to the job's destination. The
// keep is only reported complete once the export finishes, so the process can't purge the cache
// mid-copy.
func (e *Engine) finalizeKeep(t *torrent.Torrent, job *keepJob) {
	files := []*torrent.File{job.file}
	if job.file == nil {
		files = t.Files()
	}

	failed := 0
	for _, f := range files {
		dst := job.path
		if job.file == nil && t.Info().IsDir() {
			dst = filepath.Join(job.path, filepath.FromSlash(f.DisplayPath()))
		}
		if err := e.exportFile(t, f, dst); err != nil {
			slog.Error("failed to export kept file", "err", err, "dest", dst)
			failed++
		}
	}

	e.keepMu.Lock()
	job.completed = true
	close(job.done)
	e.keepMu.Unlock()

	if failed > 0 {
		SendNotification("mpv-gotorrent", fmt.Sprintf("Download finished, but %d file(s) could not be saved to %s", failed, job.path))
		return
	}
	slog.Info("keep file finished downloading to 100%", "dest", job.path)
	SendNotification("mpv-gotorrent", fmt.Sprintf("Download complete: %s", filepath.Base(job.path)))
}

// exportFile hardlinks (same filesystem) or copies a completed file out of the session cache.
func (e *Engine) exportFile(t *torrent.Torrent, f *torrent.File, dst string) error {
	src := e.cachedFilePath(t, f)

	// anacrolix stores incomplete files as "<name>.part" and renames them shortly after the
	// last piece verifies, so give the rename a moment before falling back to the .part file.
	deadline := time.Now().Add(10 * time.Second)
	for !fileExists(src) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !fileExists(src) {
		if !fileExists(src + ".part") {
			return fmt.Errorf("cached file not found: %s", src)
		}
		src += ".part"
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		// Completed cache files are made read-only; the link shares that inode.
		_ = os.Chmod(dst, 0644)
		slog.Info("hardlinked completed file to destination", "src", src, "dest", dst)
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	slog.Info("copied completed file to destination", "src", src, "dest", dst)
	return nil
}

// cachedFilePath mirrors the layout of anacrolix's default file storage:
// <dataDir>/<torrent name> for single-file torrents, <dataDir>/<torrent name>/<path> otherwise.
func (e *Engine) cachedFilePath(t *torrent.Torrent, f *torrent.File) string {
	info := t.Info()
	base := e.dataDir
	if name := info.BestName(); name != metainfo.NoName {
		base = filepath.Join(base, name)
	}
	if !info.IsDir() {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(f.DisplayPath()))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmpDst := dst + ".part"
	out, err := os.Create(tmpDst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		_ = os.Remove(tmpDst)
		return err
	}
	_ = out.Sync()
	_ = out.Close()
	return os.Rename(tmpDst, dst)
}

// Close gracefully closes the client and cleans up disk cache if configured.
func (e *Engine) Close() error {
	slog.Info("closing torrent engine")
	e.closeOnce.Do(func() { close(e.done) })
	e.freeing.Wait() // stops at its next piece once done is closed
	var errMsgs []string

	if e.ramStorage != nil {
		if at := e.ramStorage.ActiveTorrent(); at != nil {
			_ = at.Close()
		}
	}

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

// fetchTorrentURL downloads a .torrent file. Indexers like Jackett and Prowlarr often answer with
// a redirect to a magnet link instead; that link is returned in place of metainfo.
func (e *Engine) fetchTorrentURL(ctx context.Context, rawURL string) (*metainfo.MetaInfo, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme == "magnet" {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "magnet:") {
		return nil, loc, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP error: %s", resp.Status)
	}

	mi, err := metainfo.Load(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, "", err
	}
	return mi, "", nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// tempPathFor names the temp file written before renaming it over path. It is unique per process
// because the companions of several mpv windows share the cache files.
func tempPathFor(path string) string {
	return fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
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
	var buf bytes.Buffer
	mi := t.Metainfo()
	if err := mi.Write(&buf); err != nil {
		slog.Debug("failed to encode torrent metainfo", "err", err)
		return
	}
	// A companion in another mpv window may be loading this file; never let it see a partial write
	tmpPath := tempPathFor(path)
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0644); err != nil {
		slog.Debug("failed to write torrent metainfo to cache", "path", path, "err", err)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		slog.Debug("failed to write torrent metainfo to cache", "path", path, "err", err)
		return
	}
	slog.Info("cached torrent metainfo for instant reopening", "path", path)
}

// DataDir returns the path to the engine's data directory.
func (e *Engine) DataDir() string {
	return e.dataDir
}

func touchHeartbeat(dir string) {
	path := filepath.Join(dir, heartbeatFile)
	now := time.Now()
	if err := os.Chtimes(path, now, now); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(path, nil, 0644)
	}
}

// cleanStaleSessionDirs purges session directories left behind by crashed or killed sessions.
// A directory is stale once its heartbeat (or, for older versions without one, the directory
// itself) hasn't been touched for staleSessionAge, so live sessions in other mpv windows and
// background keep downloads are left alone.
func cleanStaleSessionDirs(currentDir string, parents ...string) {
	for _, parent := range parents {
		matches, err := filepath.Glob(filepath.Join(parent, "mpv-gotorrent-*"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if m == currentDir {
				continue
			}
			fi, err := os.Stat(m)
			if err != nil || !fi.IsDir() {
				continue
			}
			lastSeen := fi.ModTime()
			if hb, err := os.Stat(filepath.Join(m, heartbeatFile)); err == nil {
				lastSeen = hb.ModTime()
			}
			if time.Since(lastSeen) > staleSessionAge {
				slog.Debug("cleaning stale session directory", "path", m)
				_ = os.RemoveAll(m)
			}
		}
	}
}
