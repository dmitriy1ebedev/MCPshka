package toolstats

import (
	"testing"
	"time"
)

// MCP-052: hot path of every tool call; compared by packaging/perf/compare.sh.
func BenchmarkRecord(b *testing.B) {
	s := New(time.Now())
	start := time.Now()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		s.Record("project_test", "direct", start, time.Millisecond, i%7 == 0)
	}
}
