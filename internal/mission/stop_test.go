package mission

import (
	"testing"
	"time"
)

func TestEveryRunningKindNamesItsStop(t *testing.T) {
	now := time.Now()
	r := Evaluate(now, []Observation{
		{Kind: "job", ID: "mwv1-2a", Path: "/root/repos/x", Phase: "running", EvidenceAt: now},
		{Kind: "agent_task", ID: "t1", Path: "/root/repos/x", Phase: "running", EvidenceAt: now, Running: true},
		{Kind: "integration", ID: "i1", Path: "/root/repos/x", Phase: "queued"},
		{Kind: "lease", ID: "l1", Path: "/root/repos/x", Phase: "writer_held", EvidenceAt: now, Running: true},
	}, nil, true)
	want := map[string]string{"job": "job_cancel", "agent_task": "agent_task_cancel", "integration": "integration_cancel"}
	for _, i := range r.Items {
		if w := want[i.Kind]; w != "" && (i.Stop == nil || i.Stop.Tool != w) {
			t.Errorf("%s: stop %+v, want %s", i.Kind, i.Stop, w)
		}
		if i.Kind == "lease" && i.Stop != nil {
			t.Errorf("a lease is released by its holder, not stopped from the list: %+v", i.Stop)
		}
		if i.Kind == "job" && i.State != "ok" {
			t.Errorf("a live job is ok, got %s %v", i.State, i.BlockedBy)
		}
	}
}
