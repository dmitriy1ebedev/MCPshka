package perfbudget

import (
	"strings"
	"testing"
)

const baseRun = `goos: linux
BenchmarkRecord-4   	 1000000	      1000 ns/op	      48 B/op	       1 allocs/op
BenchmarkRecord-4   	 1000000	      1200 ns/op	      48 B/op	       1 allocs/op
BenchmarkMarkDone-4 	  200000	      5000 ns/op
PASS`

// MCP-052: a slower or more allocating candidate fails the budget; a vanished
// benchmark fails too; noise inside the budget passes.
func TestCompareRejectsRegressionAllocsAndMissing(t *testing.T) {
	base, err := Parse(strings.NewReader(baseRun))
	if err != nil || base["BenchmarkRecord"].NsPerOp != 1100 || base["BenchmarkRecord"].Samples != 2 {
		t.Fatalf("parse: %+v %v", base, err)
	}
	ok, _ := Parse(strings.NewReader("BenchmarkRecord-4 1 1150 ns/op 48 B/op 1 allocs/op\nBenchmarkMarkDone-4 1 4000 ns/op\n"))
	if d, err := Compare(base, ok, 15); err != nil || d[0].Verdict != "IMPROVED" || d[1].Verdict != "OK" {
		t.Fatalf("within budget: %+v %v", d, err)
	}
	slow, _ := Parse(strings.NewReader("BenchmarkRecord-4 1 1500 ns/op 48 B/op 1 allocs/op\nBenchmarkMarkDone-4 1 5000 ns/op\n"))
	if _, err := Compare(base, slow, 15); err == nil || !strings.Contains(err.Error(), "PERF_BUDGET_EXCEEDED") {
		t.Fatalf("regression passed: %v", err)
	}
	allocs, _ := Parse(strings.NewReader("BenchmarkRecord-4 1 1000 ns/op 96 B/op 2 allocs/op\nBenchmarkMarkDone-4 1 5000 ns/op\n"))
	if _, err := Compare(base, allocs, 15); err == nil {
		t.Fatal("extra allocation passed")
	}
	gone, _ := Parse(strings.NewReader("BenchmarkRecord-4 1 1000 ns/op 48 B/op 1 allocs/op\n"))
	if _, err := Compare(base, gone, 15); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing benchmark passed: %v", err)
	}
}
