package db

import (
	"context"
	"testing"

	"github.com/muradImre/database-design/btreeidx"
)

func newStore() *Database {
	return New("testStore", func() DBIndex[string, Document] {
		return btreeidx.New[string, Document]()
	})
}

func TestWriteReadDocument(t *testing.T) {
	s := newStore()

	created, err := s.WriteDocument("user1", "doc1", []byte(`{"name":"Ada"}`), true)
	if err != nil || !created {
		t.Fatalf("write doc1: created=%v err=%v", created, err)
	}

	doc, err := s.ReadDocument("doc1")
	if err != nil {
		t.Fatalf("read doc1: %v", err)
	}
	if string(doc.GetDocData()) != `{"name":"Ada"}` {
		t.Fatalf("unexpected data: %s", doc.GetDocData())
	}
	if _, err := s.ReadDocument("missing"); err == nil {
		t.Fatal("expected error reading missing document")
	}
	if _, err := s.WriteDocument("user1", "", []byte(`{}`), true); err == nil {
		t.Fatal("expected error writing empty id")
	}
}

func TestOverwriteSemantics(t *testing.T) {
	s := newStore()
	s.WriteDocument("creator", "doc1", []byte(`{"v":1}`), true)
	orig, _ := s.ReadDocument("doc1")
	origMD := orig.GetDocMetadata()
	createdAt := origMD.CreatedAt()

	// if_exists=fail behavior: overwrite=false on existing returns (false, nil).
	wrote, err := s.WriteDocument("other", "doc1", []byte(`{"v":2}`), false)
	if wrote || err != nil {
		t.Fatalf("no-overwrite existing should be (false,nil), got (%v,%v)", wrote, err)
	}
	cur, _ := s.ReadDocument("doc1")
	if string(cur.GetDocData()) != `{"v":1}` {
		t.Fatalf("value should be unchanged, got %s", cur.GetDocData())
	}

	// Overwrite preserves creation metadata, advances the updater.
	if _, err := s.WriteDocument("editor", "doc1", []byte(`{"v":2}`), true); err != nil {
		t.Fatal(err)
	}
	upd, _ := s.ReadDocument("doc1")
	md := upd.GetDocMetadata()
	if md.CreatedBy() != "creator" || md.CreatedAt() != createdAt {
		t.Fatalf("creation metadata not preserved: by=%s at=%d", md.CreatedBy(), md.CreatedAt())
	}
	if md.LastModifiedBy() != "editor" {
		t.Fatalf("expected updated_by=editor, got %s", md.LastModifiedBy())
	}
}

func TestListDocumentsOrderedAndRanged(t *testing.T) {
	s := newStore()
	for _, id := range []string{"c", "a", "e", "b", "d"} {
		s.WriteDocument("u", id, []byte(`{}`), true)
	}

	all, err := s.ListDocuments("", "", context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c", "d", "e"}
	if len(all) != len(want) {
		t.Fatalf("expected %d docs, got %d", len(want), len(all))
	}
	for i, e := range all {
		if e.ID != want[i] {
			t.Fatalf("order wrong at %d: got %s want %s", i, e.ID, want[i])
		}
	}

	ranged, _ := s.ListDocuments("b", "d", context.Background())
	if len(ranged) != 3 || ranged[0].ID != "b" || ranged[2].ID != "d" {
		t.Fatalf("range [b,d] wrong: %+v", ranged)
	}
}

func TestDeleteDocument(t *testing.T) {
	s := newStore()
	s.WriteDocument("u", "doc1", []byte(`{}`), true)
	if err := s.DeleteDocument("doc1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.ReadDocument("doc1"); err == nil {
		t.Fatal("document should be gone")
	}
	if err := s.DeleteDocument("doc1"); err == nil {
		t.Fatal("deleting missing doc should error")
	}
}
