package approvedtask

import (
	"context"
	"errors"
	"mcpshka/internal/rootbroker"
	"strings"
	"testing"
)

func stubApprovedHost(t *testing.T, unit string, httpCode int, dialErr error) *int {
	t.Helper()
	calls := 0
	oldU, oldH, oldD := approvedUnitState, approvedHTTP, approvedDial
	t.Cleanup(func() { approvedUnitState, approvedHTTP, approvedDial = oldU, oldH, oldD })
	approvedUnitState = func(context.Context, string) (string, error) { calls++; return unit, nil }
	approvedHTTP = func(context.Context, string) (int, error) { calls++; return httpCode, nil }
	approvedDial = func(context.Context, string) error { return dialErr }
	return &calls
}

var noisePlan = rootbroker.ApprovedPlan{Units: []string{"go2rtc-ha-noise.service"}, Probe: "http://192.168.77.10:8795/activity"}

// 27.10 HA noise bridge: unit active, /activity 200, yet the task FAILED on
// its own sandboxed urllib check. The host observation is evidence, but an
// active unit may predate the run: effect stays unknown, no replay.
func TestViewApprovedVerifiesFailedRunFromHost(t *testing.T) {
	stubApprovedHost(t, "active", 200, nil)
	v := view(context.Background(), rootbroker.ApprovedResult{State: "FAILED", Effect: "unknown", Plan: noisePlan})
	if v.Effect != "unknown" || v.Observed != "declared_state_present" || len(v.Verify) != 2 || !strings.Contains(v.NextSafeAction, "do not execute again") {
		t.Fatalf("%+v", v)
	}
	stubApprovedHost(t, "inactive", 200, nil)
	v = view(context.Background(), rootbroker.ApprovedResult{State: "FAILED", Effect: "unknown", Plan: noisePlan})
	if v.Effect != "unknown" || v.Observed != "" || v.Verify[0].State != "FAIL" {
		t.Fatalf("inactive unit observed healthy: %+v", v)
	}
	stubApprovedHost(t, "active", 503, nil)
	v = view(context.Background(), rootbroker.ApprovedResult{State: "DONE", Effect: "applied", Plan: noisePlan})
	if v.State != "VERIFY_FAILED" || v.Effect != "applied" || !strings.Contains(v.NextSafeAction, "verify FAILS") {
		t.Fatalf("failing probe after DONE hidden: %+v", v)
	}
}

func TestViewApprovedSkipsHostChecksWhenNothingRan(t *testing.T) {
	calls := stubApprovedHost(t, "active", 200, nil)
	v := view(context.Background(), rootbroker.ApprovedResult{State: "FAILED", Effect: "none", Plan: noisePlan})
	if *calls != 0 || v.Verify != nil {
		t.Fatalf("checked a task that never ran: %d %+v", *calls, v)
	}
	v = view(context.Background(), rootbroker.ApprovedResult{State: "AWAITING_FACE_ID", Plan: noisePlan})
	if *calls != 0 || v.Verify != nil {
		t.Fatal("checked a task awaiting approval")
	}
}

// 27.10 LG: Face ID passed, the TV was off, the task FAILED. An
// unreachable target is a HOLD before the approval is spent.
func TestReachPreflightHoldsOnUnreachableTarget(t *testing.T) {
	stubApprovedHost(t, "", 0, errors.New("connect: no route to host"))
	err := reachPreflight(context.Background(), rootbroker.ApprovedPlan{Reach: []string{"192.168.77.40:9922"}})
	if err == nil || !strings.Contains(err.Error(), "192.168.77.40:9922 unreachable") {
		t.Fatalf("%v", err)
	}
	stubApprovedHost(t, "", 0, nil)
	if err := reachPreflight(context.Background(), rootbroker.ApprovedPlan{Reach: []string{"192.168.77.40:9922"}}); err != nil {
		t.Fatal(err)
	}
	if err := reachPreflight(context.Background(), rootbroker.ApprovedPlan{Reach: []string{"8.8.8.8:53"}}); err == nil {
		t.Fatal("public target dialled")
	}
}
