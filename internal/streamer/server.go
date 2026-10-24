package streamer

import (
	"context"
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

	"github.com/biohazard786/mpv-gotorrent/internal/engine"
	"github.com/biohazard786/mpv-gotorrent/internal/playlist"
)

// Server coordinates HTTP streaming of torrent files and metadata API.
type Server struct {
	engine     *engine.Engine
	httpServer *http.Server
	listener   net.Listener
	baseURL    string
	port       int

	allFiles   []playlist.FileItem
	videoFiles []playlist.FileItem
	mu         sync.RWMutex
	stopChan   chan struct{}
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

// NewServer creates a new streamer HTTP server.
func NewServer(eng *engine.Engine, requestedPort int) (*Server, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", requestedPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind HTTP server on %s: %w", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)

	s := &Server{
		engine:     eng,
		listener:   listener,
		baseURL:    baseURL,
		port:       actualPort,
		stopChan:   make(chan struct{}),
	}

	// Index files from torrent
	t := eng.Torrent()
	if t != nil && t.Info() != nil {
		all, videos := playlist.ParseFiles(t.Files(), baseURL)
		s.allFiles = all
		s.videoFiles = videos
		slog.Debug("indexed torrent files", "total", len(all), "videos", len(videos))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/stream/", s.handleStream)
	mux.HandleFunc("/sub", s.handleSubtitle)
	mux.HandleFunc("/sub/", s.handleSubtitle)
	mux.HandleFunc("/playlist", s.handlePlaylist)
	mux.HandleFunc("/meta", s.handleMeta)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/shutdown", s.handleShutdown)

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Streaming needs unbounded write time
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("HTTP streaming server initialized", "base_url", baseURL)
	return s, nil
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

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	var fileIdx int
	var err error

	if strings.HasPrefix(r.URL.Path, "/stream/") {
		trimmed := strings.TrimPrefix(r.URL.Path, "/stream/")
		parts := strings.Split(trimmed, "/")
		if len(parts) > 0 && parts[0] != "" {
			fileIdx, err = strconv.Atoi(parts[0])
		} else {
			err = fmt.Errorf("missing file index in path")
		}
	} else {
		fileIdxStr := r.URL.Query().Get("file")
		fileIdx, err = strconv.Atoi(fileIdxStr)
	}

	if err != nil {
		slog.Warn("invalid stream request", "path", r.URL.Path, "query", r.URL.RawQuery)
		http.Error(w, "invalid or missing file query param", http.StatusBadRequest)
		return
	}

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

	// Prioritize a 32MB sliding window ahead of read pointer
	reader.SetReadahead(32 * 1024 * 1024)
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

func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	var fileIdx int
	var err error

	if strings.HasPrefix(r.URL.Path, "/sub/") {
		trimmed := strings.TrimPrefix(r.URL.Path, "/sub/")
		parts := strings.Split(trimmed, "/")
		if len(parts) > 0 && parts[0] != "" {
			fileIdx, err = strconv.Atoi(parts[0])
		} else {
			err = fmt.Errorf("missing subtitle index in path")
		}
	} else {
		fileIdxStr := r.URL.Query().Get("file")
		fileIdx, err = strconv.Atoi(fileIdxStr)
	}

	if err != nil {
		http.Error(w, "invalid file parameter", http.StatusBadRequest)
		return
	}

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
	s.mu.RLock()
	videos := s.videoFiles
	s.mu.RUnlock()

	format := r.URL.Query().Get("format")
	if format == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(videos)
		return
	}

	// Standard M3U playlist format
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=\"playlist.m3u8\"")

	fmt.Fprintf(w, "#EXTM3U\n")
	for _, v := range videos {
		fmt.Fprintf(w, "#EXTINF:-1,%s\n", v.Name)
		fmt.Fprintf(w, "%s\n", v.StreamURL)
	}
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
		select {
		case s.stopChan <- struct{}{}:
		default:
		}
	}()
}
