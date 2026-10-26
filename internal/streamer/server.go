package streamer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/biohazard786/mpv-gotorrent/internal/engine"
	"github.com/biohazard786/mpv-gotorrent/internal/playlist"
)

// TokenHeader carries the per-session token required by state-changing POST endpoints.
const TokenHeader = "X-Gotorrent-Token"

// Server coordinates HTTP streaming of torrent files and metadata API.
type Server struct {
	engine     *engine.Engine
	httpServer *http.Server
	listener   net.Listener
	baseURL    string
	port       int
	token      string

	allFiles   []playlist.FileItem
	videoFiles []playlist.FileItem
	mu         sync.RWMutex
	stopChan   chan struct{}
	stopOnce   sync.Once
}

// MetadataResponse contains high-level torrent and file metadata.
type MetadataResponse struct {
	Name           string              `json:"name"`
	InfoHash       string              `json:"infohash"`
	TotalSize      int64               `json:"total_size"`
	AllFilesCount  int                 `json:"all_files_count"`
	VideoCount     int                 `json:"video_count"`
	PlaylistURL    string              `json:"playlist_url"`
	FirstStreamURL string              `json:"first_stream_url,omitempty"`
	Videos         []playlist.FileItem `json:"videos"`
	Files          []playlist.FileItem `json:"files"`
}

func listenTCPWithFallback(requestedPort int) (net.Listener, int, error) {
	if requestedPort <= 0 {
		requestedPort = 8989
	}

	for p := requestedPort; p < requestedPort+20; p++ {
		addr := fmt.Sprintf("127.0.0.1:%d", p)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			return l, p, nil
		}
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to bind HTTP server to free port: %w", err)
	}
	return l, l.Addr().(*net.TCPAddr).Port, nil
}

// NewServer creates a new streamer HTTP server.
func NewServer(eng *engine.Engine, requestedPort int) (*Server, error) {
	listener, actualPort, err := listenTCPWithFallback(requestedPort)
	if err != nil {
		return nil, fmt.Errorf("failed to bind HTTP server: %w", err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)

	s := &Server{
		engine:     eng,
		listener:   listener,
		baseURL:    baseURL,
		port:       actualPort,
		token:      rand.Text(),
		stopChan:   make(chan struct{}),
	}

	// Index files from torrent
	t := eng.Torrent()
	if t != nil && t.Info() != nil {
		infoHash := t.InfoHash().HexString()
		all, videos := playlist.ParseFiles(t.Files(), baseURL, infoHash)
		s.allFiles = all
		s.videoFiles = videos
		slog.Debug("indexed torrent files", "total", len(all), "videos", len(videos))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/torrent/", s.handleTorrentRoute)
	mux.HandleFunc("/playlist", s.handlePlaylist)
	mux.HandleFunc("/meta", s.handleMeta)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/keep", s.handleKeep)
	mux.HandleFunc("/media", s.handleMedia)
	mux.HandleFunc("/prefetch", s.handlePrefetch)
	mux.HandleFunc("/shutdown", s.handleShutdown)

	s.httpServer = &http.Server{
		Handler:      s.guard(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Streaming needs unbounded write time
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("HTTP streaming server initialized", "base_url", baseURL, "port", actualPort)
	return s, nil
}

// guard rejects requests whose Host isn't this loopback server, which blocks DNS-rebinding
// attacks from web pages, and requires the session token on POST endpoints so a page can't
// trigger /keep or /shutdown with a cross-site form post.
func (s *Server) guard(next http.Handler) http.Handler {
	allowedHosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.port): true,
		fmt.Sprintf("localhost:%d", s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && r.Header.Get(TokenHeader) != s.token {
			http.Error(w, "invalid or missing token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Token returns the per-session token clients must send in TokenHeader on POST requests.
func (s *Server) Token() string {
	return s.token
}

// Start begins serving HTTP requests in the background.
func (s *Server) Start() {
	go func() {
		if err := s.httpServer.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "err", err)
		}
	}()
}

// BaseURL returns the local base URL (e.g. http://127.0.0.1:54321).
func (s *Server) BaseURL() string {
	return s.baseURL
}

// Port returns the bound port.
func (s *Server) Port() int {
	return s.port
}

// VideoFiles returns the list of playable video files.
func (s *Server) VideoFiles() []playlist.FileItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.videoFiles
}

// AllFiles returns all files inside the torrent.
func (s *Server) AllFiles() []playlist.FileItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allFiles
}

// StopChan returns a channel triggered when shutdown is requested.
func (s *Server) StopChan() <-chan struct{} {
	return s.stopChan
}

// Stop closes the HTTP listener and server.
func (s *Server) Stop() error {
	slog.Info("stopping HTTP streaming server")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// M3UPlaylist generates an M3U format playlist with clean titles and deterministic URLs.
func (s *Server) M3UPlaylist() string {
	s.mu.RLock()
	videos := s.videoFiles
	s.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, v := range videos {
		cleanTitle := strings.TrimSuffix(v.Name, filepath.Ext(v.Name))
		sb.WriteString(fmt.Sprintf("#EXTINF:-1,%s\n", cleanTitle))
		sb.WriteString(fmt.Sprintf("%s\n", v.StreamURL))
	}
	return sb.String()
}

func (s *Server) handleTorrentRoute(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/torrent/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		http.Error(w, "invalid torrent route path", http.StatusBadRequest)
		return
	}

	// Route 1: /torrent/<infohash>/playlist
	if parts[1] == "playlist" {
		s.handlePlaylist(w, r)
		return
	}

	// Route 2: /torrent/<infohash>/sub/<subIdx>/<name>
	if parts[1] == "sub" {
		if len(parts) < 3 {
			http.Error(w, "missing subtitle index", http.StatusBadRequest)
			return
		}
		subIdx, err := strconv.Atoi(parts[2])
		if err != nil {
			http.Error(w, "invalid subtitle index", http.StatusBadRequest)
			return
		}
		s.serveSubtitleFile(w, r, subIdx)
		return
	}

	// Route 3: /torrent/<infohash>/<fileIdx>/<name>
	fileIdx, err := strconv.Atoi(parts[1])
	if err != nil {
		http.Error(w, "invalid file index", http.StatusBadRequest)
		return
	}

	s.serveTorrentStream(w, r, fileIdx)
}

func (s *Server) serveTorrentStream(w http.ResponseWriter, r *http.Request, fileIdx int) {
	t := s.engine.Torrent()
	if t == nil {
		slog.Warn("stream request rejected: torrent not ready")
		http.Error(w, "torrent not ready", http.StatusServiceUnavailable)
		return
	}

	files := t.Files()
	if fileIdx < 0 || fileIdx >= len(files) {
		slog.Warn("file index out of bounds", "index", fileIdx, "max", len(files))
		http.Error(w, "file index out of bounds", http.StatusNotFound)
		return
	}

	s.engine.PrioritizeFile(fileIdx)

	targetFile := files[fileIdx]
	rangeHeader := r.Header.Get("Range")
	slog.Debug("streaming file chunk",
		"file", targetFile.DisplayPath(),
		"size", targetFile.Length(),
		"range", rangeHeader,
		"client", r.RemoteAddr,
	)

	reader := targetFile.NewReader()
	defer reader.Close()

	// Prioritize a sliding window ahead of the read pointer. It is re-evaluated on every read, so
	// it grows from the default to a bitrate-sized window once mpv reports the file's duration.
	reader.SetReadaheadFunc(func(torrent.ReadaheadContext) int64 {
		return s.engine.Readahead(fileIdx)
	})
	reader.SetResponsive()

	// Content-Type determination
	ext := filepath.Ext(targetFile.DisplayPath())
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		switch strings.ToLower(ext) {
		case ".mkv":
			contentType = "video/x-matroska"
		case ".mp4":
			contentType = "video/mp4"
		case ".webm":
			contentType = "video/webm"
		case ".avi":
			contentType = "video/x-msvideo"
		case ".ts":
			contentType = "video/mp2t"
		default:
			contentType = "application/octet-stream"
		}
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(targetFile.DisplayPath())))

	modTime := time.Unix(0, 0)
	if meta := t.Metainfo(); meta.CreationDate != 0 {
		modTime = time.Unix(meta.CreationDate, 0)
	}

	http.ServeContent(w, r, targetFile.DisplayPath(), modTime, reader)
}

func (s *Server) serveSubtitleFile(w http.ResponseWriter, r *http.Request, fileIdx int) {
	t := s.engine.Torrent()
	if t == nil {
		http.Error(w, "torrent not ready", http.StatusServiceUnavailable)
		return
	}

	files := t.Files()
	if fileIdx < 0 || fileIdx >= len(files) {
		http.Error(w, "file index out of bounds", http.StatusNotFound)
		return
	}

	targetFile := files[fileIdx]
	slog.Debug("serving subtitle", "file", targetFile.DisplayPath(), "client", r.RemoteAddr)

	reader := targetFile.NewReader()
	defer reader.Close()

	reader.SetReadahead(1 * 1024 * 1024)
	reader.SetResponsive()

	ext := filepath.Ext(targetFile.DisplayPath())
	contentType := "text/plain; charset=utf-8"
	if ext == ".vtt" {
		contentType = "text/vtt"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Accept-Ranges", "bytes")

	modTime := time.Unix(0, 0)
	http.ServeContent(w, r, targetFile.DisplayPath(), modTime, reader)
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		s.mu.RLock()
		videos := s.videoFiles
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(videos)
		return
	}

	// Standard M3U playlist format
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=\"playlist.m3u8\"")
	_, _ = w.Write([]byte(s.M3UPlaylist()))
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	videos := s.videoFiles
	all := s.allFiles
	s.mu.RUnlock()

	t := s.engine.Torrent()
	name := ""
	hash := ""
	totalSize := int64(0)
	if t != nil {
		name = t.Name()
		hash = t.InfoHash().HexString()
		totalSize = t.Length()
	}

	firstStream := ""
	if len(videos) > 0 {
		firstStream = videos[0].StreamURL
	}

	resp := MetadataResponse{
		Name:           name,
		InfoHash:       hash,
		TotalSize:      totalSize,
		AllFilesCount:  len(all),
		VideoCount:     len(videos),
		PlaylistURL:    s.baseURL + "/playlist",
		FirstStreamURL: firstStream,
		Videos:         videos,
		Files:          all,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats := s.engine.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slog.Info("shutdown requested via HTTP endpoint")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"shutting_down"}`))

	go func() {
		time.Sleep(100 * time.Millisecond)
		s.stopOnce.Do(func() { close(s.stopChan) })
	}()
}

// handleMedia receives a file's playback duration from mpv, which sizes its readahead by bitrate.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fileIdx, err := strconv.Atoi(r.URL.Query().Get("file_idx"))
	if err != nil {
		http.Error(w, "invalid file index", http.StatusBadRequest)
		return
	}
	duration, err := strconv.ParseFloat(r.URL.Query().Get("duration"), 64)
	if err != nil {
		http.Error(w, "invalid duration", http.StatusBadRequest)
		return
	}

	bitrate, err := s.engine.SetFileDuration(fileIdx, duration)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{
		"bitrate":   bitrate,
		"readahead": s.engine.Readahead(fileIdx),
	})
}

// handlePrefetch starts downloading the opening of a file mpv will play next.
func (s *Server) handlePrefetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fileIdx, err := strconv.Atoi(r.URL.Query().Get("file_idx"))
	if err != nil {
		http.Error(w, "invalid file index", http.StatusBadRequest)
		return
	}

	queued, err := s.engine.PrefetchFile(fileIdx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"file_idx": int64(fileIdx), "bytes": queued})
}

func (s *Server) handleKeep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "current"
	}

	targetDir := r.URL.Query().Get("dir")
	fileIdxStr := r.URL.Query().Get("file_idx")
	fileIdx := -1
	if fileIdxStr != "" {
		if idx, err := strconv.Atoi(fileIdxStr); err == nil {
			fileIdx = idx
		}
	}

	status, err := s.engine.KeepCurrent(scope, targetDir, fileIdx)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(status)
}

