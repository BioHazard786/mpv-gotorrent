package engine

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// TrackersURL is the daily-tested best IP trackers list by ngosang.
	TrackersURL = "https://raw.githubusercontent.com/ngosang/trackerslist/master/trackers_best_ip.txt"
	// TrackerCacheDuration defines how frequently the trackers list should be updated.
	TrackerCacheDuration = 7 * 24 * time.Hour
)

// DefaultTrackers contains high-reliability public trackers baked directly into the binary.
// This guarantees instant public tracker boost even on fresh installs or offline runs.
var DefaultTrackers = []string{
	// Top high-uptime IP-based trackers (bypasses DNS resolution entirely)
	"udp://185.121.168.96:1337/announce",
	"udp://138.186.10.167:1337/announce",
	"udp://34.66.57.33:80/announce",
	"udp://34.66.57.33:1337/announce",
	"udp://23.157.120.14:6969/announce",
	"udp://209.141.59.25:6969/announce",
	"udp://60.172.236.18:6969/announce",
	"udp://15.235.207.99:8081/announce",
	"udp://178.239.19.29:80/announce",
	"udp://31.59.141.120:6969/announce",
	"udp://45.38.170.167:6969/announce",
	"udp://47.76.201.250:6969/announce",
	"udp://74.81.245.60:2010/announce",
	"udp://160.30.240.158:1337/announce",

	// Top tier-1 public domain trackers
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://open.tracker.cl:1337/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
}

// TrackerManager manages loading, caching, and background updating of public BitTorrent trackers.
type TrackerManager struct {
	cacheDir   string
	trackers   [][]string
	mu         sync.RWMutex
	updating   atomic.Bool
	httpClient *http.Client
}

// NewTrackerManager creates a TrackerManager initialized with cached or default public trackers.
func NewTrackerManager(cacheDir string) *TrackerManager {
	tm := &TrackerManager{
		cacheDir: cacheDir,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
	tm.load()
	return tm
}

// GetTrackers returns the current public tracker announce tiers.
func (tm *TrackerManager) GetTrackers() [][]string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.trackers
}

func (tm *TrackerManager) trackersFilePath() string {
	return filepath.Join(tm.cacheDir, "trackers.txt")
}

// load loads trackers from local cache file if valid; otherwise uses embedded defaults.
// It triggers a non-blocking background update if the cache file is stale (> 7 days) or missing.
func (tm *TrackerManager) load() {
	path := tm.trackersFilePath()
	info, err := os.Stat(path)

	var trackerList []string
	if err == nil && info.Size() > 0 {
		if content, readErr := os.ReadFile(path); readErr == nil {
			trackerList = parseTrackers(string(content))
		}
	}

	if len(trackerList) == 0 {
		trackerList = append([]string(nil), DefaultTrackers...)
		// Write initial default list to cache
		_ = tm.saveTrackers(trackerList)
	}

	tm.mu.Lock()
	tm.trackers = toTiers(trackerList)
	tm.mu.Unlock()

	// If missing or older than TrackerCacheDuration, trigger background update
	if err != nil || time.Since(info.ModTime()) > TrackerCacheDuration {
		go tm.updateInBackground()
	}
}

// updateInBackground fetches the latest trackers from ngosang/trackerslist asynchronously.
func (tm *TrackerManager) updateInBackground() {
	if !tm.updating.CompareAndSwap(false, true) {
		return
	}
	defer tm.updating.Store(false)

	req, err := http.NewRequest(http.MethodGet, TrackersURL, nil)
	if err != nil {
		slog.Debug("failed to create trackers request", "err", err)
		return
	}
	req.Header.Set("User-Agent", "mpv-gotorrent")

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		slog.Debug("failed to fetch updated trackers list", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("tracker list fetch returned non-200", "status", resp.Status)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		slog.Debug("failed to read tracker response body", "err", err)
		return
	}

	parsed := parseTrackers(string(body))
	if len(parsed) < 5 {
		slog.Debug("fetched trackers list is too small, retaining existing trackers", "count", len(parsed))
		return
	}

	// Save to disk
	if err := tm.saveTrackers(parsed); err != nil {
		slog.Debug("failed to save updated trackers list to cache", "err", err)
		return
	}

	tm.mu.Lock()
	tm.trackers = toTiers(parsed)
	tm.mu.Unlock()

	slog.Info("updated public trackers cache from ngosang/trackerslist",
		"trackers_count", len(parsed),
		"path", tm.trackersFilePath(),
	)
}

func (tm *TrackerManager) saveTrackers(trackers []string) error {
	_ = os.MkdirAll(tm.cacheDir, 0755)
	path := tm.trackersFilePath()
	tmpPath := tempPathFor(path)

	data := strings.Join(trackers, "\n") + "\n"
	if err := os.WriteFile(tmpPath, []byte(data), 0644); err != nil {
		return err
	}

	// Atomic rename with Windows fallback
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(path)
		if err := os.Rename(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			return os.WriteFile(path, []byte(data), 0644)
		}
	}
	return nil
}

// parseTrackers extracts valid tracker URLs from text.
func parseTrackers(content string) []string {
	var result []string
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "udp://") &&
			!strings.HasPrefix(line, "http://") &&
			!strings.HasPrefix(line, "https://") &&
			!strings.HasPrefix(line, "wss://") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			result = append(result, line)
		}
	}
	return result
}

// toTiers packages tracker URLs into individual announce tiers.
// Each tracker in its own tier ensures the client announces to ALL trackers concurrently.
func toTiers(trackers []string) [][]string {
	tiers := make([][]string, 0, len(trackers))
	for _, tr := range trackers {
		tiers = append(tiers, []string{tr})
	}
	return tiers
}
