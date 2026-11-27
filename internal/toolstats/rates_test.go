package toolstats

import (
	"path/filepath"
	"testing"
	"time"
)

// ROADMAP P19 level 1: success rate and recovery effectiveness are exact
// counts, absent without calls, and one error never marks a tool unreliable.
func TestRatesRecoveryAndReliability(t *testing.T) {
	now := time.Now().UTC()
	s := New(now)
	defer s.Persist(filepath.Join(t.TempDir(), "usage.json"), time.Hour)()
	call := func(tool string, failed bool) { s.Record(tool, "direct", now, time.Millisecond, failed) }

	call("flaky", true)
	if Unreliable(s.History("flaky")) {
		t.Fatal("one error marked the tool unreliable")
	}
	call("flaky", false) // recovered
	call("flaky", true)
	call("flaky", true) // still failing: no recovery
	for range 16 {
		call("flaky", true)
	}
	for range MinHistory {
		call("solid", false)
	}
	_, usage, _ := s.Report([]string{"flaky", "solid", "idle"}, 0)
	by := map[string]Usage{}
	for _, u := range usage {
		by[u.Tool] = u
	}
	f := by["flaky"]
	if f.TotalCalls != 20 || f.TotalErrors != 19 || f.SuccessRate == nil || *f.SuccessRate != 0.05 || f.Recovered != 1 || f.RecoveryRate == nil || *f.RecoveryRate != 0.052 {
		t.Fatalf("flaky: %+v rate=%v recovery=%v", f, f.SuccessRate, f.RecoveryRate)
	}
	if !Unreliable(s.History("flaky")) || Unreliable(s.History("solid")) || Unreliable(s.History("idle")) {
		t.Fatal("reliability verdicts")
	}
	if by["solid"].SuccessRate == nil || *by["solid"].SuccessRate != 1 || by["solid"].RecoveryRate != nil {
		t.Fatalf("solid: %+v", by["solid"])
	}
	if by["idle"].SuccessRate != nil {
		t.Fatal("a tool nobody called has a success rate")
	}
	o := Summarize(usage)
	if o.Calls != 40 || o.Errors != 19 || *o.SuccessRate != 0.525 || o.Recovered != 1 || len(o.Unreliable) != 1 || o.Unreliable[0] != "flaky" {
		t.Fatalf("overall: %+v", o)
	}
}

// ROADMAP S5.CM baseline: calls per chain and the share of compatibility calls.
func TestChainsOf(t *testing.T) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	sp := func(sec int, route string) Span {
		return Span{Tool: "x", Route: route, StartedAt: at.Add(time.Duration(sec) * time.Second), DurationMS: 1000}
	}
	got := ChainsOf([]Span{sp(0, "direct"), sp(10, "tool_invoke"), sp(20, "direct"), sp(300, "direct"), sp(900, "exec")}, time.Minute)
	if got != (Chains{Spans: 5, Count: 3, MeanCalls: 1.6, MaxCalls: 3, Compat: 2}) {
		t.Fatalf("chains: %+v", got)
	}
	if ChainsOf(nil, time.Minute) != (Chains{}) {
		t.Fatal("empty window")
	}
}
