package persist_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/muradImre/database-design/btreeidx"
	"github.com/muradImre/database-design/db"
	"github.com/muradImre/database-design/persist"
)

func newDB(name string) *db.Database {
	return db.New(name, func() db.DBIndex[string, db.Document] {
		return btreeidx.New[string, db.Document]()
	})
}

func TestSnapshotRoundTrip(t *testing.T) {
	src := newDB("demo")
	if _, err := src.WriteDocument("murad", "person1", []byte(`{"name":"Ada","age":36}`), true); err != nil {
		t.Fatal(err)
	}
	if _, err := src.WriteDocument("murad", "person2", []byte(`{"name":"Bob","age":40}`), true); err != nil {
		t.Fatal(err)
	}

	exported, err := src.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}

	snap := persist.NewSnapshot()
	snap.Stores["demo"] = exported

	path := filepath.Join(t.TempDir(), "snap.json")
	if err := persist.Save(path, snap); err != nil {
		t.Fatal(err)
	}

	loaded, ok, err := persist.Load(path)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}

	dst := newDB("demo")
	if err := dst.ImportSnapshot(loaded.Stores["demo"]); err != nil {
		t.Fatal(err)
	}

	doc, err := dst.ReadDocument("person1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc.GetDocData()), `"Ada"`) {
		t.Fatalf("unexpected data: %s", doc.GetDocData())
	}
	meta := doc.GetDocMetadata()
	if meta.CreatedBy() != "murad" {
		t.Fatalf("metadata not restored")
	}

	if _, err := dst.ReadDocument("person2"); err != nil {
		t.Fatalf("person2 missing after restore: %v", err)
	}

	_, ok, err = persist.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || ok {
		t.Fatalf("missing file should return ok=false, got ok=%v err=%v", ok, err)
	}
}
