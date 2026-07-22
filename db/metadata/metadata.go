// Package metadata exposes a Metadata value with read-only accessors, keeping
// the creation and modification fields of a document encapsulated.
package metadata

import (
	"log/slog"
	"time"
)

// Public Metadata struct that protects access to metadata fields
type Metadata struct {
	createdBy      string
	createdAt      int64
	lastModifiedBy string
	lastModifiedAt int64
}

// Sets user to the user passed in and the created/modified  at to current time
func New(user string) Metadata {
	slog.Info("Creating new metadata...")
	return Metadata{
		createdBy:      user,
		createdAt:      time.Now().UTC().UnixNano(),
		lastModifiedBy: user,
		lastModifiedAt: time.Now().UTC().UnixNano(),
	}
}

// Restore rebuilds a Metadata from previously persisted field values, allowing
// snapshots to reconstruct the unexported fields without altering timestamps.
func Restore(createdBy string, createdAt int64, updatedBy string, updatedAt int64) Metadata {
	return Metadata{
		createdBy:      createdBy,
		createdAt:      createdAt,
		lastModifiedBy: updatedBy,
		lastModifiedAt: updatedAt,
	}
}

// Updates the metadata with the user passed in and the last modified at to current time
func Update(metadata *Metadata, user string) {
	slog.Info("Updating metadata...")
	metadata.lastModifiedBy = user
	metadata.lastModifiedAt = time.Now().UTC().UnixNano()
}

// Returns the user that created the document
func (m *Metadata) CreatedBy() string {
	return m.createdBy
}

// Returns the time the document was created
func (m *Metadata) CreatedAt() int64 {
	return m.createdAt
}

// Returns the user that last modified the document
func (m *Metadata) LastModifiedBy() string {
	return m.lastModifiedBy
}

// Returns the time the document was last modified
func (m *Metadata) LastModifiedAt() int64 {
	return m.lastModifiedAt
}
