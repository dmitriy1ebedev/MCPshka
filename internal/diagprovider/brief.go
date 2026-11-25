package diagprovider

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Signal is one DIAG agent finding (diag.agent/v1alpha1 signals[]).
type Signal struct {
	Status  string `json:"status"`
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

// ReportBrief is the part of a DIAG report a triage answer carries: counts
// and the findings that are not ok. Still UNTRUSTED evidence.
type ReportBrief struct {
	RunID      string    `json:"run_id"`
	State      string    `json:"state"`
	CapturedAt time.Time `json:"captured_at,omitempty"`
	OK         int       `json:"ok"`
	Warn       int       `json:"warn"`
	Fail       int       `json:"fail"`
	Unknown    int       `json:"unknown"`
	Findings   []Signal  `json:"findings,omitempty"`
	Omitted    int       `json:"omitted_findings,omitempty"`
}

type agentWire struct {
	Health struct {
		OK, Warn, Fail, Unknown int
	} `json:"health"`
	Signals []Signal `json:"signals"`
}

const briefFindings = 8

// Brief reduces a finished run to its non-ok findings, fail first.
func Brief(run NativeRun) ReportBrief {
	out := ReportBrief{RunID: run.ID, State: run.State, CapturedAt: run.Evidence.CapturedAt}
	var w agentWire
	if run.Report == "" || json.Unmarshal([]byte(run.Report), &w) != nil {
		return out
	}
	out.OK, out.Warn, out.Fail, out.Unknown = w.Health.OK, w.Health.Warn, w.Health.Fail, w.Health.Unknown
	bad := notOK(w.Signals)
	sort.SliceStable(bad, func(i, j int) bool { return rank(bad[i].Status) < rank(bad[j].Status) })
	if len(bad) > briefFindings {
		out.Omitted = len(bad) - briefFindings
		bad = bad[:briefFindings]
	}
	out.Findings = bad
	return out
}

func rank(status string) int {
	switch status {
	case "fail":
		return 0
	case "warn":
		return 1
	}
	return 2
}

func notOK(signals []Signal) []Signal {
	var out []Signal
	for _, s := range signals {
		if s.Status != "ok" && s.Key != "" {
			s.Summary = strings.ToValidUTF8(s.Summary[:min(len(s.Summary), 240)], "")
			out = append(out, s)
		}
	}
	return out
}

// Comparison is the before/after of two runs by signal key.
type Comparison struct {
	Before     string   `json:"before_run_id"`
	After      string   `json:"after_run_id"`
	Resolved   []Signal `json:"resolved"`
	Appeared   []Signal `json:"appeared"`
	Persisting []Signal `json:"persisting"`
	Verdict    string   `json:"verdict" jsonschema:"FIXED, IMPROVED, UNCHANGED, WORSE or INCOMPARABLE."`
}

// Compare tells what a fix changed: findings gone, new and still present.
func Compare(before, after NativeRun) Comparison {
	out := Comparison{Before: before.ID, After: after.ID}
	var b, a agentWire
	if before.Report == "" || after.Report == "" || json.Unmarshal([]byte(before.Report), &b) != nil || json.Unmarshal([]byte(after.Report), &a) != nil {
		out.Verdict = "INCOMPARABLE"
		return out
	}
	was := map[string]Signal{}
	for _, s := range notOK(b.Signals) {
		was[s.Key] = s
	}
	now := map[string]bool{}
	for _, s := range notOK(a.Signals) {
		now[s.Key] = true
		if _, ok := was[s.Key]; ok {
			out.Persisting = append(out.Persisting, s)
		} else {
			out.Appeared = append(out.Appeared, s)
		}
	}
	for _, s := range notOK(b.Signals) {
		if !now[s.Key] {
			out.Resolved = append(out.Resolved, s)
		}
	}
	switch {
	case len(out.Appeared) > 0:
		out.Verdict = "WORSE"
	case len(out.Resolved) > 0 && len(out.Persisting) == 0:
		out.Verdict = "FIXED"
	case len(out.Resolved) > 0:
		out.Verdict = "IMPROVED"
	default:
		out.Verdict = "UNCHANGED"
	}
	return out
}

// Latest returns the newest bound run for target started within the window.
func (n *Native) Latest(ctx context.Context, target string, within time.Duration) (NativeRun, bool, error) {
	if n.manager.Database() == nil {
		return NativeRun{}, false, ErrIncompatible
	}
	keys, err := n.manager.Database().List("diag/v1/runs/")
	if err != nil {
		return NativeRun{}, false, err
	}
	var best nativeIntent
	for _, k := range keys {
		in, err := n.intent("workflow-" + strings.TrimPrefix(k, "diag/v1/runs/"))
		if err != nil || in.Input.Target != target || time.Since(in.StartedAt) > within || !in.StartedAt.After(best.StartedAt) {
			continue
		}
		best = in
	}
	if best.ID == "" {
		return NativeRun{}, false, nil
	}
	run, err := n.Status(ctx, best.ID)
	return run, err == nil, err
}
