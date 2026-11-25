package searchindex

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// legacyFitReply preserves the pre-optimization reply fitter as an oracle.
func legacyFitReply(hits []Hit, maxBytes int) ([]Hit, error) {
	for len(hits) > 0 {
		raw, err := json.Marshal(hits)
		if err != nil {
			return nil, err
		}
		if len(raw) <= maxBytes {
			return hits, nil
		}
		if len(hits) == 1 {
			return nil, ErrBudgetExceeded
		}
		hits = hits[:len(hits)-1]
	}
	return hits, nil
}

// Baseline uses the unchanged algorithm; candidate delegates to fitReply.
var benchmarkFitReply = fitReply

func replyFixture(n int, text string) []Hit {
	hits := make([]Hit, n)
	for i := range hits {
		hits[i] = Hit{Record: Record{ID: fmt.Sprintf("%04d", i), Text: text,
			Source: "fixture", Provenance: "benchmark", EvidenceRefs: []string{"synthetic"}}, Score: 1}
	}
	return hits
}

func BenchmarkReplyBudget(b *testing.B) {
	for _, data := range []struct{ name, text string }{
		{"ascii", strings.Repeat("x", 200)},
		{"escaped", strings.Repeat("<>&\n\t\"", 30)},
		{"unicode", strings.Repeat("\u754c\U0001f642\u2028", 30)},
	} {
		for _, n := range []int{0, 1, 20, 100} {
			hits := replyFixture(n, data.text)
			for _, trim := range []bool{false, true} {
				if trim && n < 20 {
					continue
				}
				budget := 65536
				mode := "budget64KiB"
				if trim {
					raw, _ := json.Marshal(hits[:1])
					budget = len(raw)
					mode = "trim"
				}
				b.Run(fmt.Sprintf("%s/hits%d/%s", data.name, n, mode), func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						got, err := benchmarkFitReply(hits, budget)
						if err != nil || (n > 0 && len(got) == 0) {
							b.Fatal(len(got), err)
						}
					}
				})
			}
		}
	}
}

func BenchmarkSearchReplyBudget(b *testing.B) {
	for _, n := range []int{100, 1000} {
		for _, selected := range []int{0, 1, 20, 100} {
			for _, trim := range []bool{false, true} {
				if trim && selected < 20 {
					continue
				}
				hits := replyFixture(n, strings.Repeat("x", 200))
				records := make(fixture, n)
				for i := range records {
					records[i] = hits[i].Record
				}
				q := Query{Limit: selected, MaxBytes: 65536}
				if selected == 0 {
					q.Limit = 100
					q.Text = "absent"
				}
				mode := "fit"
				if trim {
					raw, _ := json.Marshal(hits[:1])
					q.MaxBytes = len(raw)
					mode = "trim"
				}
				b.Run(fmt.Sprintf("records%d/hits%d/%s", n, selected, mode), func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						got, err := Search(context.Background(), records, q, n)
						if err != nil || (selected == 0 && len(got) != 0) {
							b.Fatal(len(got), err)
						}
					}
				})
			}
		}
	}
}

func BenchmarkSearchReplyBudgetLatency(b *testing.B) {
	hits := replyFixture(100, strings.Repeat("x", 200))
	records := make(fixture, len(hits))
	for i := range records {
		records[i] = hits[i].Record
	}
	raw, _ := json.Marshal(hits[:1])
	q := Query{Limit: 20, MaxBytes: len(raw)}
	const samples = 1000
	elapsed := make([]int64, samples)
	b.ReportAllocs()
	b.ResetTimer()
	for batch := 0; batch < b.N; batch++ {
		for i := range elapsed {
			start := time.Now()
			got, err := Search(context.Background(), records, q, len(records))
			elapsed[i] = time.Since(start).Nanoseconds()
			if err != nil || len(got) != 1 {
				b.Fatal(len(got), err)
			}
		}
	}
	b.StopTimer()
	sort.Slice(elapsed, func(i, j int) bool { return elapsed[i] < elapsed[j] })
	b.ReportMetric(float64(elapsed[samples/2]), "p50-ns")
	b.ReportMetric(float64(elapsed[95*samples/100]), "p95-ns")
	b.ReportMetric(samples, "calls/batch")
}
