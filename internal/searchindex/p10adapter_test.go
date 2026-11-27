package searchindex

import (
	"context"
	"errors"
	"mcpshka/internal/knowledge"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func record(id, visible string) knowledge.Record {
	return knowledge.Record{ID: id, ExportMetadata: true, Kind: "incident", Source: "diag", Scope: "h96", Provenance: "capture", Timestamp: time.Now().UTC(), Fields: map[string]knowledge.Field{
		"public":  {Value: visible, Export: true},
		"private": {Value: "private-marker", Export: false},
		"secret":  {Value: "secret-marker", Export: true, Secret: true},
	}}
}
func TestP10SourceVisibilityForgetSupersede(t *testing.T) {
	st, e := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = st.Add(record("a", "old")); e != nil {
		t.Fatal(e)
	}
	src := P10Source{Store: st, MaxBytes: 4096}
	var idx Index
	if e = idx.Reindex(context.Background(), src, 5, 4096); e != nil {
		t.Fatal(e)
	}
	h, e := idx.Search(context.Background(), Query{Text: "old", Limit: 5})
	if e != nil || len(h) != 1 {
		t.Fatalf("%+v %v", h, e)
	}
	for _, word := range []string{"private-marker", "secret-marker"} {
		h, e = idx.Search(context.Background(), Query{Text: word, Limit: 5})
		if e != nil || len(h) != 0 {
			t.Fatalf("leaked %s: %+v %v", word, h, e)
		}
	}
	if e = st.Supersede("a", record("b", "new")); e != nil {
		t.Fatal(e)
	}
	if e = idx.Reindex(context.Background(), src, 5, 4096); e != nil {
		t.Fatal(e)
	}
	h, e = idx.Search(context.Background(), Query{Text: "old", Limit: 5})
	if e != nil || len(h) != 0 {
		t.Fatal(h, e)
	}
	h, e = idx.Search(context.Background(), Query{Text: "new", Limit: 5})
	if e != nil || len(h) != 1 || h[0].Record.ID != "b" {
		t.Fatal(h, e)
	}
	if e = st.Forget("b"); e != nil {
		t.Fatal(e)
	}
	if e = idx.Reindex(context.Background(), src, 5, 4096); e != nil {
		t.Fatal(e)
	}
	h, e = idx.Search(context.Background(), Query{Text: "new", Limit: 5})
	if e != nil || len(h) != 0 {
		t.Fatal(h, e)
	}
}
func TestP10SourceBudgets(t *testing.T) {
	st, e := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = st.Add(record("a", strings.Repeat("x", 2048))); e != nil {
		t.Fatal(e)
	}
	src := P10Source{Store: st, MaxBytes: 512}
	var idx Index
	if e = idx.Reindex(context.Background(), src, 10, 4096); !errors.Is(e, knowledge.ErrLimit) {
		t.Fatal(e)
	}
	if idx.Health().Ready {
		t.Fatal("partial index published")
	}
	src.MaxBytes = 4096
	if e = idx.Reindex(context.Background(), src, 10, 4096); e != nil {
		t.Fatal(e)
	}
	if e = st.Add(record("b", "another")); e != nil {
		t.Fatal(e)
	}
	before := idx.Health()
	if e = idx.Reindex(context.Background(), src, 1, 4096); !errors.Is(e, knowledge.ErrLimit) {
		t.Fatal(e)
	}
	if idx.Health() != before {
		t.Fatal("failed reindex replaced projection")
	}
}

func TestP10SourcePrivateMetadataAndAutomaticInvalidation(t *testing.T) {
	st, e := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.json"))
	if e != nil {
		t.Fatal(e)
	}
	public := record("public", "visible-marker")
	private := record("private", "private-owner-marker")
	private.ExportMetadata = false
	if e = st.Add(public); e != nil {
		t.Fatal(e)
	}
	if e = st.Add(private); e != nil {
		t.Fatal(e)
	}
	src := P10Source{Store: st}
	var idx Index
	if e = idx.Reindex(context.Background(), src, 10, DefaultMaxBytes); e != nil {
		t.Fatal(e)
	}
	hits, e := idx.Search(context.Background(), Query{Limit: 10})
	if e != nil || len(hits) != 1 || hits[0].Record.ID != "public" {
		t.Fatalf("private metadata escaped: %+v %v", hits, e)
	}
	if e = st.Forget("public"); e != nil {
		t.Fatal(e)
	}
	if _, e = idx.Search(context.Background(), Query{Limit: 10}); !errors.Is(e, ErrStale) {
		t.Fatalf("P10Source ignored forget revision: %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = src.Snapshot(ctx, 10); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancelled source admitted: %v", e)
	}
}
