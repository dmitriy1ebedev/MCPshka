package releaseapplystatus

import (
	"encoding/json"
	"errors"
	"testing"

	"mcpshka/internal/releasequeue"
)

func TestReleaseApplyRootAuditOverridesLiveVersion(t *testing.T) {
	for _, phase := range []string{"ROLLBACK_FAILED", "UNKNOWN"} {
		out := Output{Version: "0.13.5", Installed: "0.13.5", State: "INSTALLED", Queue: &releasequeue.View{Stage: "DONE"}, Stages: releasequeue.Stages{ProductionVerified: true}}
		raw, _ := json.Marshal(map[string]string{"state": "HOLD", "last_phase": phase, "reason": "root audit " + phase, "error": "exit=1", "traceback": "original traceback"})
		Reconcile(&out, raw, nil)
		if out.State != "HOLD" || out.Stages.ProductionVerified || out.Queue.Stage == "DONE" || out.Error != "exit=1" || out.Traceback == "" || out.Reason == "" {
			t.Fatal(out)
		}
	}
	out := Output{Version: "0.13.5", Installed: "0.13.5", State: "INSTALLED"}
	Reconcile(&out, nil, errors.New("broker down"))
	if out.State != "HOLD" {
		t.Fatal(out)
	}
	Reconcile(&out, []byte(`{"state":"SUCCEEDED","last_phase":"SUCCEEDED"}`), nil)
	if out.State != "INSTALLED" {
		t.Fatal(out)
	}
}
