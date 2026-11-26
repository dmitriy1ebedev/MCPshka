package searchindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReplyBudgetMatchesLegacy(t *testing.T) {
	for _, text := range []string{"ascii", "<>&\n\t\"\\", "\u754c\U0001f642\u2028\u2029", string([]byte{0xff, 0, 0xfe})} {
		for _, n := range []int{0, 1, 20, 100} {
			hits := replyFixture(n, text)
			for i := range hits {
				hits[i].Record.Text = strings.Repeat(text, 1+i%7)
			}
			before := append([]Hit(nil), hits...)
			budgets := []int{1, 2, 32768, 65536}
			for k := 1; k <= n; k++ {
				if n == 100 && k > 2 && k != 19 && k != 20 && k != 50 && k < 99 {
					continue
				}
				raw, err := json.Marshal(hits[:k])
				if err != nil {
					t.Fatal(err)
				}
				budgets = append(budgets, len(raw)-1, len(raw), len(raw)+1)
			}
			for _, budget := range budgets {
				got, err := benchmarkFitReply(hits, budget)
				want, wantErr := legacyFitReply(hits, budget)
				if !reflect.DeepEqual(got, want) || !errors.Is(err, wantErr) {
					t.Fatalf("text=%q n=%d budget=%d: got %d/%v want %d/%v", text, n, budget, len(got), err, len(want), wantErr)
				}
				if err == nil && len(got) > 0 {
					raw, _ := json.Marshal(got)
					if len(raw) > budget {
						t.Fatalf("reply uses %d bytes, budget %d", len(raw), budget)
					}
				}
			}
			if n > 0 && !reflect.DeepEqual(hits, before) {
				t.Fatal("fitting mutated input")
			}
		}
	}
	for _, hits := range [][]Hit{nil, {}} {
		got, err := benchmarkFitReply(hits, 1)
		if err != nil || !reflect.DeepEqual(got, hits) {
			t.Fatalf("empty slice changed: %#v %v", got, err)
		}
	}
}

func TestReplyBudgetPreservesMarshalErrors(t *testing.T) {
	invalid := []time.Time{
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("bad", 24*60*60)),
	}
	for _, timestamp := range invalid {
		for _, position := range []int{0, 1, 19} {
			hits := replyFixture(20, "small")
			hits[position].Record.Timestamp = timestamp
			for _, budget := range []int{1, 600, 65536} {
				got, err := benchmarkFitReply(hits, budget)
				_, wantErr := legacyFitReply(hits, budget)
				var marshalErr *json.MarshalerError
				if got != nil || !errors.As(err, &marshalErr) || wantErr == nil || err.Error() != wantErr.Error() {
					t.Fatalf("position=%d budget=%d: got %#v %v; want %v", position, budget, got, err, wantErr)
				}
			}
		}
	}
}

func TestSearchReplyBudgetOrderingAndEmpty(t *testing.T) {
	records := fixture{
		{ID: "c", Text: "hit", Source: "test", Provenance: "test"},
		{ID: "b", Text: "hit hit", Source: "test", Provenance: "test"},
		{ID: "a", Text: "hit hit", Source: "test", Provenance: "test"},
	}
	full, err := Search(context.Background(), records, Query{Text: "hit", Limit: 3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(full[0].Record.ID, full[1].Record.ID, full[2].Record.ID) != "abc" {
		t.Fatal(full)
	}
	raw, _ := json.Marshal(full[:2])
	got, err := Search(context.Background(), records, Query{Text: "hit", Limit: 3, MaxBytes: len(raw)}, 3)
	if err != nil || !reflect.DeepEqual(got, full[:2]) {
		t.Fatalf("%v %v", got, err)
	}
	empty, err := Search(context.Background(), records, Query{Text: "miss", Limit: 3, MaxBytes: 1}, 3)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("zero-hit behavior: %#v %v", empty, err)
	}
}

type replyCountingSource struct {
	records fixture
	calls   int
}

func (s *replyCountingSource) Snapshot(context.Context, int) ([]Record, error) {
	s.calls++
	return s.records, nil
}

func TestSearchReplyBudgetRetains100Hits(t *testing.T) {
	source := &replyCountingSource{}
	for _, hit := range replyFixture(100, strings.Repeat("x", 200)) {
		source.records = append(source.records, hit.Record)
	}
	got, err := Search(context.Background(), source, Query{Limit: 100, MaxBytes: 65536}, 100)
	if err != nil || len(got) != 100 || source.calls != 1 {
		t.Fatalf("got %d hits, %d snapshots, %v", len(got), source.calls, err)
	}
	raw, err := json.Marshal(got)
	if err != nil || len(raw) > 65536 {
		t.Fatalf("%d bytes, %v", len(raw), err)
	}
}
