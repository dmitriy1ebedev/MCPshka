package searchindex

import (
	"context"
	"strings"
	"testing"
)

func TestSearchFitsReplyToByteBudget(t *testing.T) {
	var recs []Record
	for i := 0; i < 20; i++ {
		recs = append(recs, Record{ID: "r" + string(rune('a'+i)), Kind: "note", Text: "gitverse " + strings.Repeat("x", 500), Source: "test", Provenance: "test", Revision: "v1"})
	}
	hits, err := Search(context.Background(), indexSource{records: recs}, Query{Text: "gitverse", Limit: 20, MaxBytes: 3000}, 100)
	if err != nil || len(hits) == 0 || len(hits) >= 20 {
		t.Fatalf("reply not trimmed to budget: %d hits, %v", len(hits), err)
	}
	if _, err := Search(context.Background(), indexSource{records: recs}, Query{Text: "gitverse", Limit: 5, MaxBytes: 100}, 100); err == nil {
		t.Fatal("single oversized hit must still fail")
	}
}
