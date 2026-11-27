package releasequeue

import "testing"

// A release never reports more than it proved: integrated only past SHIP,
// verified only when DONE and the running controller has the version, and
// acceptance is never a release stage.
func TestReleaseStagesStayApart(t *testing.T) {
	if st := StagesOf(nil, "0.12.6", "0.12.6"); st.ImplementationComplete || st.ProductionVerified {
		t.Fatalf("no queue: %+v", st)
	}
	q := &View{Stage: "SHIP", PreviousVersion: "0.12.5"}
	if st := StagesOf(q, "0.12.5", "0.12.6"); !st.ImplementationComplete || st.Integrated || st.Rollback != "0.12.5" {
		t.Fatalf("ship: %+v", st)
	}
	q.Stage = "DONE"
	if st := StagesOf(q, "0.12.5", "0.12.6"); !st.Integrated || st.ProductionVerified {
		t.Fatalf("done but another version runs: %+v", st)
	}
	if st := StagesOf(q, "0.12.6", "0.12.6"); !st.ProductionVerified || st.Accepted {
		t.Fatalf("done: %+v", st)
	}
}
