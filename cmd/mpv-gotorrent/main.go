package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/biohazard786/mpv-gotorrent/internal/engine"
	"github.com/biohazard786/mpv-gotorrent/internal/streamer"
	"github.com/biohazard786/mpv-gotorrent/internal/watchdog"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

type ReadyBanner struct {
	Status         string      `json:"status"`
	Port           int         `json:"port"`
	BaseURL        string      `json:"base_url"`
	PlaylistURL    string      `json:"playlist_url"`
	FirstStreamURL string      `json:"first_stream_url"`
	M3UPlaylist    string      `json:"m3u_playlist"`
	Name           string      `json:"name"`
	InfoHash       string      `json:"infohash"`
	TotalSize      int64       `json:"total_size"`
	VideosCount    int         `json:"videos_count"`
	FilesCount     int         `json:"files_count"`
	Videos         interface{} `json:"videos"`
	StorageMode    string      `json:"storage_mode"`
	Token          string      `json:"token,omitempty"`
	Error          string      `json:"error,omitempty"`
}

// writeStateFile writes via a temp file and rename so the polling Lua script never reads a
// half-written file. It is private to the user since it carries the session token.
func writeStateFile(filePath string, data interface{}) {
	if filePath == "" {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("failed to encode state file", "err", err)
		return
	}
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0600); err != nil {
		slog.Error("failed to create state file", "path", filePath, "err", err)
		return
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		_ = os.WriteFile(filePath, payload, 0600)
	}
}

func parseLogLevel(lvl string) slog.Level {
	switch strings.ToLower(lvl) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func setupLogger(logLevelStr string) {
	w := os.Stderr
	level := parseLogLevel(logLevelStr)
	isTerm := isatty.IsTerminal(w.Fd()) || isatty.IsCygwinTerminal(w.Fd())

	logger := slog.New(tint.NewHandler(w, &tint.Options{
		Level:      level,
		TimeFormat: time.TimeOnly,
		NoColor:    !isTerm,
	}))

	slog.SetDefault(logger)
}

// Version is set during build with -ldflags="-X main.Version=vX.Y.Z"
var Version = "dev"

func main() {
	var (
		source       = flag.String("source", "", "Torrent source (magnet link, .torrent file path, http(s) URL, or infohash)")
		parentPID    = flag.Int("parent-pid", 0, "PID of parent mpv process for watchdog monitoring")
		port         = flag.Int("port", 8989, "Local HTTP port to bind to (default: 8989)")
		cacheDir     = flag.String("cache-dir", "", "Directory to store torrent cache files")
		keepFiles    = flag.Bool("keep-files", false, "Do not delete downloaded files on exit")
		maxRAMMB     = flag.Int("max-ram-mb", 0, "Max RAM in MB for memory-only ring buffer (0 = use disk cache)")
		keepDir      = flag.String("keep-dir", "", "Default directory to save kept torrent files (default: ~/Videos)")
		keepScope    = flag.String("keep-scope", "current", "Keep scope ('current' file or 'all' files)")
		detachOnKeep = flag.Bool("detach-on-keep", true, "Continue downloading in background when mpv closes if kept")
		timeoutSec   = flag.Int("timeout", 60, "Seconds to wait for torrent metadata")
		jsonOutput   = flag.Bool("json-output", true, "Output JSON ready banner on stdout for mpv script")
		stateFile    = flag.String("state-file", "", "Optional file path to write ready/error status JSON")
		logLevel     = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
		showVersion  = flag.Bool("version", false, "Print version and exit")
		showVersionS = flag.Bool("v", false, "Print version and exit (shorthand)")
	)
	flag.Parse()

	if *showVersion || *showVersionS {
		fmt.Printf("mpv-gotorrent %s\n", Version)
		os.Exit(0)
	}

	setupLogger(*logLevel)

	if *source == "" {
		slog.Error("missing required flag", "flag", "--source")
		flag.Usage()
		writeStateFile(*stateFile, ReadyBanner{Status: "error", Error: "--source is required"})
		os.Exit(1)
	}

	slog.Info("starting mpv-gotorrent companion",
		"parent_pid", *parentPID,
		"keep_files", *keepFiles,
		"max_ram_mb", *maxRAMMB,
		"timeout", fmt.Sprintf("%ds", *timeoutSec),
	)

	var eng *engine.Engine
	var srv *streamer.Server

	cleanup := func() {
		slog.Info("shutting down companion")
		if srv != nil {
			_ = srv.Stop()
		}
		if eng != nil {
			_ = eng.Close()
		}
	}

	// The watchdog only reports that mpv exited; whether to shut down or keep downloading in the
	// background is decided below, after it fires.
	wd := watchdog.New(*parentPID, nil)
	wd.Start()

	ctx, stopSignals := signal.NotifyContext(wd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var err error
	eng, err = engine.NewEngine(engine.Config{
		DataDir:   *cacheDir,
		KeepFiles: *keepFiles,
		MaxRAMMB:  *maxRAMMB,
		KeepDir:   *keepDir,
		KeepScope: *keepScope,
		Port:      0, // Random BitTorrent peer port
	})
	if err != nil {
		slog.Error("failed to initialize torrent engine", "err", err)
		writeStateFile(*stateFile, ReadyBanner{Status: "error", Error: err.Error()})
		os.Exit(1)
	}

	metaCtx, metaCancel := context.WithTimeout(ctx, time.Duration(*timeoutSec)*time.Second)
	defer metaCancel()

	slog.Info("fetching torrent metadata from swarm", "source", *source)
	t, err := eng.AddSource(metaCtx, *source)
	if err != nil {
		slog.Error("failed to load torrent source", "err", err)
		writeStateFile(*stateFile, ReadyBanner{Status: "error", Error: err.Error()})
		eng.Close()
		os.Exit(1)
	}

	srv, err = streamer.NewServer(eng, *port)
	if err != nil {
		slog.Error("failed to create HTTP streamer server", "err", err)
		writeStateFile(*stateFile, ReadyBanner{Status: "error", Error: err.Error()})
		eng.Close()
		os.Exit(1)
	}

	srv.Start()

	videoFiles := srv.VideoFiles()
	allFiles := srv.AllFiles()

	firstStream := ""
	if len(videoFiles) > 0 {
		firstStream = videoFiles[0].StreamURL
	} else if len(allFiles) > 0 {
		firstStream = fmt.Sprintf("%s/torrent/%s/0/%s", srv.BaseURL(), t.InfoHash().HexString(), url.PathEscape(allFiles[0].Name))
	}

	banner := ReadyBanner{
		Status:         "ready",
		Port:           srv.Port(),
		BaseURL:        srv.BaseURL(),
		PlaylistURL:    srv.BaseURL() + "/playlist",
		FirstStreamURL: firstStream,
		M3UPlaylist:    srv.M3UPlaylist(),
		Name:           t.Name(),
		InfoHash:       t.InfoHash().HexString(),
		TotalSize:      t.Length(),
		VideosCount:    len(videoFiles),
		FilesCount:     len(allFiles),
		Videos:         videoFiles,
		StorageMode:    eng.Stats().StorageMode,
		Token:          srv.Token(),
	}

	slog.Info("stream ready",
		"name", banner.Name,
		"infohash", banner.InfoHash,
		"total_bytes", banner.TotalSize,
		"videos", banner.VideosCount,
		"stream_url", banner.FirstStreamURL,
		"playlist_url", banner.PlaylistURL,
		"storage_mode", banner.StorageMode,
	)

	writeStateFile(*stateFile, banner)

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(banner); err != nil {
			slog.Error("failed to encode JSON banner to stdout", "err", err)
		}
		_ = os.Stdout.Sync()
	}

	// Wait for mpv to exit, mpv to ask us to stop (e.g. it switched torrents), or a signal
	select {
	case <-ctx.Done():
		if wd.Context().Err() == nil {
			// An explicit signal always stops, even mid-keep
			slog.Info("received termination signal")
			cleanup()
			return
		}
		slog.Info("parent mpv process exited")
	case <-srv.StopChan():
		slog.Info("HTTP shutdown endpoint requested termination")
	}
	stopSignals()

	if *detachOnKeep && eng.IsKeepActive() && !eng.IsKeepCompleted() {
		slog.Info("detaching to complete kept download in background", "dest", eng.KeepPath())
		engine.SendNotification("mpv-gotorrent", fmt.Sprintf("Continuing background download of %s...", eng.KeepName()))

		// Release HTTP streamer port so next mpv session can use it immediately
		_ = srv.Stop()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		select {
		case <-eng.WaitKeepComplete():
			slog.Info("background keep download finished successfully")
		case sig := <-sigChan:
			slog.Info("background download interrupted by signal", "signal", sig.String())
		}
	}

	cleanup()
}
