// Package persist defines the on-disk JSON snapshot format for the document
// store and helpers for reading and writing snapshot files. The tree types are
// shared between the db and dbServer packages so a full store can be serialized
// and later reloaded.
package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SnapshotVersion is the current snapshot schema version.
const SnapshotVersion = 2

// Snapshot is the top-level document written to disk.
type Snapshot struct {
	Version int                      `json:"version"`
	SavedAt string                   `json:"saved_at"`
	Stores  map[string]StoreSnapshot `json:"stores"`
}

// StoreSnapshot holds the documents of a single flat store.
type StoreSnapshot struct {
	Documents map[string]DocumentSnapshot `json:"documents"`
}

// DocumentSnapshot captures a document's raw JSON data and metadata.
type DocumentSnapshot struct {
	Data     json.RawMessage  `json:"data"`
	Metadata MetadataSnapshot `json:"metadata"`
}

// MetadataSnapshot mirrors the metadata fields persisted for each document.
type MetadataSnapshot struct {
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt int64  `json:"updated_at"`
}

// NewSnapshot returns an empty snapshot stamped with the current time.
func NewSnapshot() Snapshot {
	return Snapshot{
		Version: SnapshotVersion,
		SavedAt: time.Now().UTC().Format(time.RFC3339),
		Stores:  make(map[string]StoreSnapshot),
	}
}

// Load reads and decodes a snapshot from path. It returns (Snapshot, false, nil)
// when the file does not exist so callers can treat that as a fresh start.
func Load(path string) (Snapshot, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, false, err
	}
	return snap, true, nil
}

// Save writes the snapshot to path, creating parent directories as needed. The
// file is written to a temporary path and renamed to make the update atomic.
func Save(path string, snap Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
