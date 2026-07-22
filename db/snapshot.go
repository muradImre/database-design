package db

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/muradImre/database-design/db/metadata"
	"github.com/muradImre/database-design/persist"
)

// snapshotMaxKey is the sentinel upper bound used to query every key in an index.
var snapshotMaxKey = strings.Repeat("\U0010FFFF", 2000)

// ExportSnapshot serializes every document in the store.
func (d *Database) ExportSnapshot() (persist.StoreSnapshot, error) {
	pairs, err := d.docs.Query(context.Background(), "", snapshotMaxKey)
	if err != nil {
		return persist.StoreSnapshot{}, err
	}
	docs := make(map[string]persist.DocumentSnapshot, len(pairs))
	for _, p := range pairs {
		md := p.Value.metadata
		docs[p.Key] = persist.DocumentSnapshot{
			Data: json.RawMessage(p.Value.data),
			Metadata: persist.MetadataSnapshot{
				CreatedBy: md.CreatedBy(),
				CreatedAt: md.CreatedAt(),
				UpdatedBy: md.LastModifiedBy(),
				UpdatedAt: md.LastModifiedAt(),
			},
		}
	}
	return persist.StoreSnapshot{Documents: docs}, nil
}

// ImportSnapshot rebuilds the store from a snapshot, preserving persisted
// metadata rather than resetting timestamps.
func (d *Database) ImportSnapshot(s persist.StoreSnapshot) error {
	for id, ds := range s.Documents {
		md := metadata.Restore(ds.Metadata.CreatedBy, ds.Metadata.CreatedAt, ds.Metadata.UpdatedBy, ds.Metadata.UpdatedAt)
		doc := newDocumentFromParts(md, []byte(ds.Data))
		if _, err := d.docs.Upsert(id, func(key string, curr Document, exists bool) (Document, error) {
			return doc, nil
		}); err != nil {
			return err
		}
	}
	return nil
}
