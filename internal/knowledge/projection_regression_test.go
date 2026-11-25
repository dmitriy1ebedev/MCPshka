package knowledge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomaticCredentialProjection(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	r := record("safe")
	r.Fields["note"] = Field{Value: "Bearer PRIVATE_CANARY", Export: true}
	r.Fields["api_key"] = Field{Value: "PRIVATE_KEY", Export: true}
	if e = s.Add(r); e != nil {
		t.Fatal(e)
	}
	data, e := s.Export()
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "PRIVATE_") {
		t.Fatal("unlabelled credential exported")
	}
	bad := record("unsafe-metadata")
	bad.Provenance = "authorization: PRIVATE_META"
	if e = s.Add(bad); e != nil {
		t.Fatal(e)
	}
	data, e = s.Export()
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "PRIVATE_META") {
		t.Fatal("credential metadata exported")
	}
}
func TestSupersessionPreservesPortableLineage(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	a := record("old")
	if e = s.Add(a); e != nil {
		t.Fatal(e)
	}
	b := record("new")
	b.Provenance = "old"
	b.EvidenceRefs = []string{"old"}
	b.Fields["public"] = Field{Value: "current fact", Export: true}
	if e = s.Supersede("old", b); e != nil {
		t.Fatal(e)
	}
	data, e := s.Export()
	if e != nil {
		t.Fatal("supersession broke export", e)
	}
	var out []Record
	if e = json.Unmarshal(data, &out); e != nil {
		t.Fatal(e)
	}
	dest, e := Open(filepath.Join(t.TempDir(), "cold.db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = dest.Import(data); e != nil {
		t.Fatal("portable lineage import", e)
	}
	if _, _, e = dest.Projection(context.Background(), 100, MaxBytes); e != nil {
		t.Fatal(e)
	}
	hits, e := dest.Search(context.Background(), Query{Limit: 10})
	if e != nil || len(hits) != 1 || hits[0].ID != "new" {
		t.Fatal("superseded active", hits, e)
	}
}
