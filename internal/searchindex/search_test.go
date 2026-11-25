package searchindex

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture []Record

func (f fixture) Snapshot(_ context.Context, _ int) ([]Record, error) { return f, nil }
func TestIndexEvidenceIsolation(t *testing.T) {
	original := fixture{{ID: "a", Provenance: "probe", Source: "p10", Revision: "1", Timestamp: time.Now(), EvidenceRefs: []string{"proof-original"}, Text: "route"}}
	var idx Index
	if err := idx.Reindex(context.Background(), original, 1, 4096); err != nil {
		t.Fatal(err)
	}
	initial := idx.Health().Digest
	original[0].EvidenceRefs[0] = "tampered-source"
	hits, err := idx.Search(context.Background(), Query{Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].Record.EvidenceRefs[0] != "proof-original" {
		t.Fatalf("source mutated index: %+v %v", hits, err)
	}
	hits[0].Record.EvidenceRefs[0] = "tampered-result"
	again, err := idx.Search(context.Background(), Query{Limit: 1})
	if err != nil || again[0].Record.EvidenceRefs[0] != "proof-original" || idx.Health().Digest != initial {
		t.Fatalf("result mutated index: %+v %v", again, err)
	}
}

func TestBoundedSearch(t *testing.T) {
	f := fixture{{ID: "b", Text: "network route", Provenance: "probe", Source: "diag", Kind: "incident", Project: "lane"}, {ID: "a", Text: "network network route", Provenance: "probe", Source: "audit", Kind: "incident", Project: "lane"}, {ID: "c", Text: "network", Provenance: "probe", Source: "audit", Superseded: true}, {ID: "d", Text: "network", Provenance: "probe", Source: ""}}
	hits, err := Search(context.Background(), f, Query{Text: "NETWORK", Kind: "incident", Project: "lane", Limit: 1}, 4)
	if err != nil || len(hits) != 1 || hits[0].Record.ID != "a" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}
func TestLimitsAndCancellation(t *testing.T) {
	f := fixture{{ID: "a", Provenance: "probe", Source: "x", Text: "ok"}}
	for _, q := range []Query{{Limit: 0}, {Limit: 101}} {
		if _, err := Search(context.Background(), f, q, 2); !errors.Is(err, ErrInvalidLimit) {
			t.Fatal(err)
		}
	}
	if _, err := Search(context.Background(), f, Query{Limit: 1}, 0); !errors.Is(err, ErrInvalidBudget) {
		t.Fatal(err)
	}
	if _, err := Search(context.Background(), nil, Query{Limit: 1}, 2); !errors.Is(err, ErrNoSource) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, f, Query{Limit: 1}, 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestScanBudget(t *testing.T) {
	f := fixture{{ID: "a", Provenance: "probe", Source: "x", Text: "miss"}, {ID: "b", Provenance: "probe", Source: "x", Text: "hit"}}
	hits, err := Search(context.Background(), f, Query{Text: "hit", Limit: 2}, 1)
	if !errors.Is(err, ErrBudgetExceeded) || len(hits) != 0 {
		t.Fatalf("%+v %v", hits, err)
	}
}

func TestReindexDeterminismAndAtomicFailure(t *testing.T) {
	a := Record{ID: "a", Text: "vpn route", Provenance: "probe", Source: "diag", Revision: "1", Timestamp: time.Now(), Artifact: "incident", Device: "h96"}
	b := Record{ID: "b", Text: "mqtt health", Provenance: "probe", Source: "audit", Revision: "2", Timestamp: time.Now()}
	var i Index
	if err := i.Reindex(context.Background(), fixture{b, a}, 2, 4096); err != nil {
		t.Fatal(err)
	}
	h := i.Health()
	if !h.Ready || h.Records != 2 || h.Bytes == 0 || len(h.Digest) != 64 {
		t.Fatal(h)
	}
	hits, err := i.Search(context.Background(), Query{Text: "vpn", Artifact: "incident", Device: "h96", Limit: 2})
	if err != nil || len(hits) != 1 || hits[0].Record.Source != "diag" {
		t.Fatalf("%+v %v", hits, err)
	}
	if err := i.Reindex(context.Background(), fixture{a, b}, 2, 4096); err != nil {
		t.Fatal(err)
	}
	if i.Health().Digest != h.Digest {
		t.Fatal("nondeterministic index")
	}
	if err := i.Reindex(context.Background(), fixture{a, b}, 2, 10); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if i.Health() != h {
		t.Fatal("failed reindex changed index")
	}
}
func TestOversizeAndInvalidProvenance(t *testing.T) {
	var i Index
	big := Record{ID: "big", Provenance: "probe", Source: "x", Text: strings.Repeat("x", MaxRecordBytes)}
	if err := i.Reindex(context.Background(), fixture{big}, 1, DefaultMaxBytes); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if err := i.Reindex(context.Background(), fixture{{ID: "bad"}}, 1, 1024); err == nil {
		t.Fatal("missing provenance accepted")
	}
	if err := i.Reindex(context.Background(), fixture{{ID: "a", Provenance: "probe", Source: "x", Revision: "1", Timestamp: time.Now()}, {ID: "a", Provenance: "probe", Source: "y", Revision: "1", Timestamp: time.Now()}}, 2, 1024); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestEmptyIndexNotReady(t *testing.T) {
	var i Index
	if i.Health().Ready {
		t.Fatal("unbuilt index reported ready")
	}
	if _, err := i.Search(context.Background(), Query{Limit: 1}); !errors.Is(err, ErrNoSource) {
		t.Fatalf("unbuilt index searchable: %v", err)
	}
}

func TestConcurrentIndexReadAndRebuild(t *testing.T) {
	var idx Index
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if n == 0 {
					if e := idx.Reindex(context.Background(), fixture{{ID: "a", Provenance: "probe", Source: "p10", Revision: "1", Timestamp: time.Now(), Text: "health"}}, 1, 4096); e != nil {
						t.Error(e)
					}
				} else {
					_, _ = idx.Search(context.Background(), Query{Text: "health", Limit: 1})
					_ = idx.Health()
				}
			}
		}(n)
	}
	wg.Wait()
	if !idx.Health().Ready {
		t.Fatal("not ready")
	}
}

func TestQueryInputBound(t *testing.T) {
	// Preserve main's tighter text/token bound while adding P11 filter limits.
	for _, q := range []Query{{Limit: 1, Text: strings.Repeat("x", 1025)}, {Limit: 1, Text: strings.Repeat("word ", 33)}} {
		if _, e := Search(context.Background(), fixture{}, q, 1); !errors.Is(e, ErrBudgetExceeded) {
			t.Fatal(e)
		}
	}
	if _, e := Search(context.Background(), fixture{}, Query{Limit: 1, Kind: strings.Repeat("x", 257)}, 1); !errors.Is(e, ErrInvalidBudget) {
		t.Fatal(e)
	}
}
func TestReindexRequiresRevisionAndTimestamp(t *testing.T) {
	var idx Index
	for _, r := range []Record{{ID: "a", Provenance: "probe", Source: "p10", Timestamp: time.Now()}, {ID: "a", Provenance: "probe", Source: "p10", Revision: "1"}} {
		if err := idx.Reindex(context.Background(), fixture{r}, 1, 4096); err == nil {
			t.Fatal("accepted missing revision/timestamp")
		}
	}
}
