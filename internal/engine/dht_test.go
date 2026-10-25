package engine

import (
	"path/filepath"
	"testing"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
)

func TestDHTNodesPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	nodesFile := filepath.Join(tmpDir, "dht.nodes")

	// Generate sample nodes
	sampleNodes := []krpc.NodeInfo{
		krpc.RandomNodeInfo(4),
		krpc.RandomNodeInfo(4),
		krpc.RandomNodeInfo(16),
	}

	if err := dht.WriteNodesToFile(sampleNodes, nodesFile); err != nil {
		t.Fatalf("failed to write nodes to file: %v", err)
	}

	readNodes, err := dht.ReadNodesFromFile(nodesFile)
	if err != nil {
		t.Fatalf("failed to read nodes from file: %v", err)
	}

	if len(readNodes) != len(sampleNodes) {
		t.Fatalf("expected %d nodes, got %d", len(sampleNodes), len(readNodes))
	}
}

func TestDHTManagerEmptySafeguard(t *testing.T) {
	tmpDir := t.TempDir()
	dm := NewDHTManager(nil, tmpDir)

	// dm.client is nil, SaveNodes should return error
	if err := dm.SaveNodes(); err == nil {
		t.Fatalf("expected error when client is nil")
	}
}
