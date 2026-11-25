package searchindex

import (
	"context"
	"encoding/json"
	"mcpshka/internal/knowledge"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeProjectionRevisionForgetAndColdReindex(t *testing.T) {
	s, _ := knowledge.Open(filepath.Join(t.TempDir(), "state.db"))
	r := knowledge.Record{ID: "router", Kind: "device", Scope: "home", Source: "diag", Provenance: "evidence-1", Timestamp: time.Now(), ExportMetadata: true, Fields: map[string]knowledge.Field{"model": {Value: "BE900", Export: true}, "private": {Value: "AUTH_TOKEN", Secret: true, Export: true}}}
	if e := s.Add(r); e != nil {
		t.Fatal(e)
	}
	src := &KnowledgeSource{Store: s}
	var a, b Index
	if e := a.Reindex(context.Background(), src, 100, DefaultMaxBytes); e != nil {
		t.Fatal(e)
	}
	if e := b.Reindex(context.Background(), src, 100, DefaultMaxBytes); e != nil {
		t.Fatal(e)
	}
	if a.Health().Digest != b.Health().Digest {
		t.Fatal("non reproducible reindex")
	}
	hits, e := a.Search(context.Background(), Query{Text: "BE900", Limit: 10})
	if e != nil || len(hits) != 1 {
		t.Fatalf("%v %v", hits, e)
	}
	raw, _ := json.Marshal(hits)
	if strings.Contains(string(raw), "AUTH_TOKEN") || hits[0].Record.Provenance != "evidence-1" {
		t.Fatal("provenance/visibility")
	}
	if e = s.Forget("router"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Search(context.Background(), Query{Limit: 10}); e != ErrStale {
		t.Fatalf("forgotten record retained %v", e)
	}
	if a.Health().Ready {
		t.Fatal("stale health ready")
	}
	if e = a.Reindex(context.Background(), src, 100, DefaultMaxBytes); e != nil {
		t.Fatal(e)
	}
	hits, e = a.Search(context.Background(), Query{Limit: 10})
	if e != nil || len(hits) != 0 {
		t.Fatal("forget not applied")
	}
}
func TestSearchOutputByteLimitAndImmutableEvidence(t *testing.T) {
	var i Index
	r := Record{ID: "a", Revision: "1", Timestamp: time.Now(), Source: "diag", Provenance: "probe", Text: strings.Repeat("x", 4096), EvidenceRefs: []string{"e"}}
	if e := i.Reindex(context.Background(), fixture{r}, 10, DefaultMaxBytes); e != nil {
		t.Fatal(e)
	}
	r.EvidenceRefs[0] = "changed"
	hits, e := i.Search(context.Background(), Query{Limit: 1, MaxBytes: 1024})
	if b, _ := json.Marshal(hits); e != nil || len(hits) != 1 || len(b) > 1024 || !strings.HasSuffix(hits[0].Record.Text, truncatedMark) {
		t.Fatalf("oversized best hit must come back truncated within the cap: %d %v", len(hits), e)
	}
	if hits, e := i.Search(context.Background(), Query{Limit: 1, MaxBytes: 16}); e != ErrBudgetExceeded || len(hits) != 0 {
		t.Fatal("output escaped byte cap")
	}
	hits, e = i.Search(context.Background(), Query{Limit: 1})
	if e != nil || hits[0].Record.EvidenceRefs[0] != "e" {
		t.Fatal("source aliases index")
	}
	hits[0].Record.EvidenceRefs[0] = "changed"
	hits, _ = i.Search(context.Background(), Query{Limit: 1})
	if hits[0].Record.EvidenceRefs[0] != "e" {
		t.Fatal("result aliases index")
	}
}
