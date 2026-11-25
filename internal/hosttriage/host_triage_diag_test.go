package hosttriage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/diagprovider"
)

type fakeLatest struct {
	run diagprovider.NativeRun
	ok  bool
	err error
}

func (f fakeLatest) Latest(context.Context, string, time.Duration) (diagprovider.NativeRun, bool, error) {
	return f.run, f.ok, f.err
}

func TestAttachDiagnosis(t *testing.T) {
	sick := func() HostTriageResult {
		return HostTriageResult{CheckedAt: time.Date(2026, 10, 23, 7, 14, 0, 0, time.UTC), Problems: []TriageProblem{{Severity: "warning", Kind: "unit", Subject: "x"}}, NextAction: "fix x"}
	}
	healthy := HostTriageResult{Healthy: true}
	AttachDiagnosis(context.Background(), &healthy, fakeLatest{err: errors.New("must not be called")})
	if healthy.DiagState != "" {
		t.Fatal("healthy host got DIAG")
	}

	none := sick()
	AttachDiagnosis(context.Background(), &none, fakeLatest{})
	last := none.Problems[len(none.Problems)-1]
	if none.DiagState != "none" || last.Kind != "diagnosis" || last.Action.Tool != "diag_investigation_start" || !strings.HasPrefix(last.Action.Args["idempotency_key"].(string), "triage-host-20261023T0710-") {
		t.Fatalf("%+v", none)
	}
	again := sick()
	again.CheckedAt = again.CheckedAt.Add(3 * time.Minute)
	AttachDiagnosis(context.Background(), &again, fakeLatest{})
	if again.Problems[1].Action.Args["idempotency_key"] != last.Action.Args["idempotency_key"] {
		t.Fatal("triage in the same window must reuse one DIAG run")
	}

	done := sick()
	AttachDiagnosis(context.Background(), &done, fakeLatest{ok: true, run: diagprovider.NativeRun{ID: "w", State: "done", Report: `{"signals":[{"status":"fail","key":"unit"}]}`}})
	if done.DiagState != "done" || done.Diagnosis == nil || len(done.Diagnosis.Findings) != 1 || len(done.Problems) != 1 {
		t.Fatalf("%+v", done)
	}

	down := sick()
	AttachDiagnosis(context.Background(), &down, fakeLatest{err: errors.New("diag missing")})
	if !strings.HasPrefix(down.DiagState, "unavailable") || len(down.Problems) != 1 {
		t.Fatalf("%+v", down)
	}
}
