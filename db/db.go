// Package db implements a flat document store: each store holds an ordered map
// of documents keyed by id. Nesting, when needed, lives inside a document's
// JSON data rather than as a separate collection resource.
package db

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"

	"github.com/muradImre/database-design/db/metadata"
	"github.com/muradImre/database-design/pair"
)

// DBIndex is an ordered key/value index over documents.
type DBIndex[K cmp.Ordered, V any] interface {
	Upsert(key K, check func(key K, currValue V, exists bool) (newValue V, err error)) (updated bool, err error)
	Remove(key K) (removedValue V, removed bool)
	Find(key K) (foundValue V, found bool)
	Query(ctx context.Context, start K, end K) (results []pair.Pair[K, V], err error)
}

// IndexFactory constructs an empty document index for a new store.
type IndexFactory func() DBIndex[string, Document]

// Document is a stored JSON document with its metadata.
type Document struct {
	metadata metadata.Metadata
	data     []byte
}

// DocumentEntry pairs a document with its id for ordered listings.
type DocumentEntry struct {
	ID       string
	Document Document
}

// Database is a single store: a flat ordered map of documents by id.
type Database struct {
	name string
	docs DBIndex[string, Document]
}

// New creates an empty store backed by an index from the supplied factory.
func New(name string, indexFactory IndexFactory) *Database {
	slog.Info("creating store", "name", name)
	return &Database{
		name: name,
		docs: indexFactory(),
	}
}

// WriteDocument creates or replaces the document with the given id. When the
// document exists and overwrite is false it returns (false, nil). On a
// successful overwrite the original creation metadata is preserved and only the
// updated-by/updated-at fields advance.
func (d *Database) WriteDocument(user, id string, data []byte, overwrite bool) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("document id must not be empty")
	}
	return d.docs.Upsert(id, func(key string, curr Document, exists bool) (Document, error) {
		if exists {
			if !overwrite {
				return curr, fmt.Errorf("document already exists")
			}
			md := curr.metadata
			metadata.Update(&md, user)
			return Document{metadata: md, data: data}, nil
		}
		return Document{metadata: metadata.New(user), data: data}, nil
	})
}

// ReadDocument returns the document with the given id.
func (d *Database) ReadDocument(id string) (Document, error) {
	doc, found := d.docs.Find(id)
	if !found {
		return Document{}, fmt.Errorf("document not found")
	}
	return doc, nil
}

// ListDocuments returns documents with id in [start, end], in ascending order.
// When both bounds are empty the entire store is returned.
func (d *Database) ListDocuments(start, end string, ctx context.Context) ([]DocumentEntry, error) {
	pairs, err := d.docs.Query(ctx, start, end)
	if err != nil {
		return nil, fmt.Errorf("error querying store: %w", err)
	}
	entries := make([]DocumentEntry, len(pairs))
	for i, p := range pairs {
		entries[i] = DocumentEntry{ID: p.Key, Document: p.Value}
	}
	return entries, nil
}

// DeleteDocument removes the document with the given id.
func (d *Database) DeleteDocument(id string) error {
	if _, removed := d.docs.Remove(id); !removed {
		return fmt.Errorf("document not found")
	}
	return nil
}

// GetDocData returns the raw JSON bytes of the document.
func (d Document) GetDocData() []byte {
	return d.data
}

// GetDocMetadata returns the document's metadata.
func (d Document) GetDocMetadata() metadata.Metadata {
	return d.metadata
}

// The following primitive accessors let transport layers read a document without
// depending on the metadata package's concrete type.

// Data returns the raw JSON bytes of the document.
func (d Document) Data() []byte { return d.data }

// CreatedAt returns the creation timestamp in milliseconds.
func (d Document) CreatedAt() int64 { return d.metadata.CreatedAt() }

// CreatedBy returns the user that created the document.
func (d Document) CreatedBy() string { return d.metadata.CreatedBy() }

// UpdatedAt returns the last-modified timestamp in milliseconds.
func (d Document) UpdatedAt() int64 { return d.metadata.LastModifiedAt() }

// UpdatedBy returns the user that last modified the document.
func (d Document) UpdatedBy() string { return d.metadata.LastModifiedBy() }

// newDocumentFromParts builds a Document from raw parts. Used by snapshot import.
func newDocumentFromParts(md metadata.Metadata, data []byte) Document {
	return Document{metadata: md, data: data}
}
