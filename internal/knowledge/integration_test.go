package knowledge

import (
	"context"
	"encoding/json"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchNeverReturnsSecretAndRecordsAreIsolated(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.db"))
	r := record("a")
	if e := s.Add(r); e != nil {
		t.Fatal(e)
	}
	r.Fields["public"] = Field{Value: "tampered", Export: true}
	r.EvidenceRefs = append(r.EvidenceRefs, "changed")
	got, e := s.Search(context.Background(), Query{Text: "mqtt", Limit: 10})
	if e != nil || len(got) != 1 {
		t.Fatalf("aliased input %v %v", got, e)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "token") {
		t.Fatal("search leaked secret")
	}
	view, _ := s.Inspect("a")
	view.Fields["public"] = Field{Value: "tampered"}
	view2, _ := s.Inspect("a")
	if view2.Fields["public"].Value != "mqtt ready" {
		t.Fatal("inspect aliases store")
	}
}
func TestSQLiteAuthorityAndColdWritersPreserveOtherRecords(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	db.PutIfAbsent("unrelated/v1/proof", []byte("preserve"))
	a, e := FromDatabase(db)
	if e != nil {
		t.Fatal(e)
	}
	b, e := FromDatabase(db)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Add(record("a")); e != nil {
		t.Fatal(e)
	}
	if e = b.Add(record("b")); e != nil {
		t.Fatal(e)
	}
	got, e := a.Snapshot(context.Background(), 100, MaxBytes)
	if e != nil || len(got) != 2 {
		t.Fatalf("lost update %v %v", got, e)
	}
	proof, _ := db.Get("unrelated/v1/proof")
	if string(proof) != "preserve" {
		t.Fatal("other namespace changed")
	}
}
func TestPortableProjectionExportImportAndForget(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.db"))
	a := record("root")
	a.ExportMetadata = true
	s.Add(a)
	child := record("derived")
	child.Provenance = "root"
	child.ExportMetadata = true
	s.Add(child)
	data, e := s.Export()
	if e != nil {
		t.Fatal(e)
	}
	dest, _ := Open(filepath.Join(t.TempDir(), "store.db"))
	if e = dest.Import(data); e != nil {
		t.Fatal(e)
	}
	snap, e := dest.Snapshot(context.Background(), 100, MaxBytes)
	if e != nil || len(snap) != 2 {
		t.Fatal(e)
	}
	for _, r := range snap {
		if _, ok := r.Fields["secret"]; ok {
			t.Fatal("secret in projection")
		}
		if r.Confidence == "" || r.Freshness == "" {
			t.Fatal("missing metadata")
		}
	}
	if e = dest.Forget("root"); e != nil {
		t.Fatal(e)
	}
	snap, e = dest.Snapshot(context.Background(), 100, MaxBytes)
	if e != nil || len(snap) != 0 {
		t.Fatal("forget retained derivatives")
	}
}
