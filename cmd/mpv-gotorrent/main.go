package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
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
	Error          string      `json:"error,omitempty"`
}

func writeStateFile(filePath string, data interface{}) {
	if filePath == "" {
		return
	}
	f, err := os.Create(filePath)
	if err != nil {
		slog.Error("failed to create state file", "path", filePath, "err", err)
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(data)
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
		source      = flag.String("source", "", "Torrent source (magnet link, .torrent file path, http(s) URL, or infohash)")
		parentPID   = flag.Int("parent-pid", 0, "PID of parent mpv process for watchdog monitoring")
		port        = flag.Int("port", 8989, "Local HTTP port to bind to (default: 8989)")
		cacheDir    = flag.String("cache-dir", "", "Directory to store torrent cache files")
		keepFiles   = flag.Bool("keep-files", false, "Do not delete downloaded files on exit")
		timeoutSec  = flag.Int("timeout", 60, "Seconds to wait for torrent metadata")
		jsonOutput  = flag.Bool("json-output", true, "Output JSON ready banner on stdout for mpv script")
		stateFile   = flag.String("state-file", "", "Optional file path to write ready/error status JSON")
		logLevel    = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
		showVersion = flag.Bool("version", false, "Print version and exit")
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

	wd := watchdog.New(*parentPID, cleanup)
	wd.Start()

	var err error
	eng, err = engine.NewEngine(engine.Config{
		DataDir:   *cacheDir,
		KeepFiles: *keepFiles,
		Port:      0, // Random BitTorrent peer port
	})
	if err != nil {
		slog.Error("failed to initialize torrent engine", "err", err)
		writeStateFile(*stateFile, ReadyBanner{Status: "error", Error: err.Error()})
		os.Exit(1)
	}

	metaCtx, metaCancel := context.WithTimeout(wd.Context(), time.Duration(*timeoutSec)*time.Second)
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
	}

	slog.Info("stream ready",
		"name", banner.Name,
		"infohash", banner.InfoHash,
		"total_bytes", banner.TotalSize,
		"videos", banner.VideosCount,
		"stream_url", banner.FirstStreamURL,
		"playlist_url", banner.PlaylistURL,
	)

	writeStateFile(*stateFile, banner)

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(banner); err != nil {
			slog.Error("failed to encode JSON banner to stdout", "err", err)
		}
		_ = os.Stdout.Sync()
	}

	// Wait for termination signal or watchdog cancellation
	select {
	case <-wd.Context().Done():
		slog.Info("watchdog context triggered termination")
	case <-srv.StopChan():
		slog.Info("HTTP shutdown endpoint requested termination")
	}

	cleanup()
}
