package engine

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent"
)

// DHTManager handles loading, persisting, and periodically flushing the DHT routing table.
type DHTManager struct {
	client   *torrent.Client
	cacheDir string
	stopSync chan struct{}
	doneSync chan struct{}
	mu       sync.Mutex
	closed   bool
}

// NewDHTManager creates a new DHTManager for the given client and persistent cache directory.
func NewDHTManager(client *torrent.Client, cacheDir string) *DHTManager {
	return &DHTManager{
		client:   client,
		cacheDir: cacheDir,
		stopSync: make(chan struct{}),
		doneSync: make(chan struct{}),
	}
}

// dhtNodesPath returns the path to the DHT nodes cache file.
func (dm *DHTManager) dhtNodesPath() string {
	return filepath.Join(dm.cacheDir, "dht.nodes")
}

// LoadNodes reads stored DHT nodes from disk and injects them directly into the DHT routing table.
// Returns the number of nodes successfully restored.
func (dm *DHTManager) LoadNodes() (int, error) {
	path := dm.dhtNodesPath()
	if !fileExists(path) {
		slog.Debug("no prior DHT cache found, starting with clean routing table", "path", path)
		return 0, nil
	}

	total := 0
	for _, ds := range dm.client.DhtServers() {
		if w, ok := ds.(torrent.AnacrolixDhtServerWrapper); ok && w.Server != nil {
			added, err := w.Server.AddNodesFromFile(path)
			if err != nil {
				slog.Warn("failed to load DHT nodes into server", "err", err, "path", path)
				return total, err
			}
			total += added
		}
	}

	if total > 0 {
		slog.Info("warmed DHT routing table from cache", "nodes", total, "path", path)
	}
	return total, nil
}

// SaveNodes persists active non-bad DHT nodes to disk.
func (dm *DHTManager) SaveNodes() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if dm.client == nil {
		return errors.New("client is nil")
	}

	var allNodes []krpc.NodeInfo
	for _, ds := range dm.client.DhtServers() {
		if w, ok := ds.(torrent.AnacrolixDhtServerWrapper); ok && w.Server != nil {
			nodes := w.Server.Nodes()
			allNodes = append(allNodes, nodes...)
		}
	}

	// Offline / empty safeguard: never overwrite healthy cache with empty data
	var validNodes []krpc.NodeInfo
	for _, n := range allNodes {
		if l := len(n.Addr.IP); l == 4 || l == 16 {
			validNodes = append(validNodes, n)
		}
	}

	if len(validNodes) == 0 {
		slog.Debug("no active DHT nodes to persist; keeping existing cache")
		return nil
	}

	_ = os.MkdirAll(dm.cacheDir, 0755)
	destPath := filepath.Join(dm.cacheDir, "dht.nodes")
	tmpPath := tempPathFor(destPath)

	if err := dht.WriteNodesToFile(validNodes, tmpPath); err != nil {
		slog.Debug("failed to write DHT nodes to temporary file", "err", err)
		return err
	}

	// Atomic rename with Windows fallback
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(destPath)
		if err := os.Rename(tmpPath, destPath); err != nil {
			_ = os.Remove(tmpPath)
			return dht.WriteNodesToFile(allNodes, destPath)
		}
	}

	slog.Debug("persisted DHT routing table to cache", "nodes", len(allNodes), "path", destPath)
	return nil
}

// StartPeriodicSync launches a background flusher that periodically saves DHT nodes.
func (dm *DHTManager) StartPeriodicSync(interval time.Duration) {
	go func() {
		defer close(dm.doneSync)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				_ = dm.SaveNodes()
			case <-dm.stopSync:
				_ = dm.SaveNodes()
				return
			}
		}
	}()
}

// Close terminates periodic sync and performs a final flush.
func (dm *DHTManager) Close() error {
	dm.mu.Lock()
	if dm.closed {
		dm.mu.Unlock()
		return nil
	}
	dm.closed = true
	close(dm.stopSync)
	dm.mu.Unlock()

	select {
	case <-dm.doneSync:
	case <-time.After(2 * time.Second):
		slog.Debug("timed out waiting for DHT periodic sync shutdown")
	}

	return dm.SaveNodes()
}
