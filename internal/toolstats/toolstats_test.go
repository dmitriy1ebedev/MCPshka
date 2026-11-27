package toolstats

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCountersPercentilesStatusAndNoPayload(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := New(t0)
	for i := 1; i <= 100; i++ {
		s.Record("job_status", "direct", t0, time.Duration(i)*time.Millisecond, i%10 == 0)
	}
	s.Record("forge_ci", "tool_invoke", t0, 12*time.Second, false)
	started, usage, spans := s.Report([]string{"job_status", "forge_ci", "rescue_start"}, 5)
	if !started.Equal(t0) || len(usage) != 3 || len(spans) != 5 {
		t.Fatalf("report: %v %d %d", started, len(usage), len(spans))
	}
	by := map[string]Usage{}
	for _, u := range usage {
		by[u.Tool] = u
	}
	js := by["job_status"]
	if js.Status != "ACTIVE" || js.Calls != 100 || js.Errors != 10 || js.P50MS != 50 || js.P95MS != 95 || js.P99MS != 99 || js.Buckets[0] != 100 {
		t.Fatalf("job_status: %+v", js)
	}
	if f := by["forge_ci"]; f.Status != "RARE" || f.CompatCalls != 1 || f.Buckets[3] != 1 {
		t.Fatalf("forge_ci: %+v", f)
	}
	if by["rescue_start"].Status != "NEVER_OBSERVED" {
		t.Fatal("registered but uncalled tool must be NEVER_OBSERVED")
	}
	raw, _ := json.Marshal(spans)
	if strings.Contains(string(raw), "argument") || strings.Contains(string(raw), "input") {
		t.Fatalf("spans must carry no payload: %s", raw)
	}
}

func BenchmarkRecordOverhead(b *testing.B) {
	s := New(time.Now())
	start := time.Now()
	for i := 0; i < b.N; i++ {
		s.Record("job_status", "direct", start, time.Millisecond, false)
	}
}
