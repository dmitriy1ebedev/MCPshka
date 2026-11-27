package mcpserver

import (
	"errors"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/workflow"
	"strings"
	"testing"
)

func TestLeaseSchedulingNeverClaimsOccupiedScopeFree(t *testing.T) {
	s := projectlease.Status{ProjectRoot: "/repo"}
	for _, tc := range []struct {
		name     string
		activity jobs.ProjectActivity
		claim    *workflow.Run
		err      error
		want     string
	}{{"pending job", jobs.ProjectActivity{Count: 1}, nil, nil, "busy"}, {"workflow claim", jobs.ProjectActivity{}, &workflow.Run{ID: "owned"}, nil, "busy"}, {"authority unavailable", jobs.ProjectActivity{}, nil, errors.New("unreadable"), "unknown"}, {"lease free requires admission", jobs.ProjectActivity{}, nil, nil, "lease_free"}} {
		t.Run(tc.name, func(t *testing.T) {
			got := leaseSchedulingStatus(s, tc.activity, tc.claim, tc.err)
			if got.WriterScopeState != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestGoneLeaseHolderNamesStaleRecovery(t *testing.T) {
	missing := func(string) bool { return true }
	alive := func(string) bool { return false }
	state := func(st string) func(string) string { return func(string) string { return st } }
	wf := projectlease.Status{ProjectRoot: "/repo", Held: true, Owner: "workflow:96926ac4", OwnerJobID: "workflow-96926ac4fcd2d1549bae4786904907eb"}
	rr := projectlease.Status{ProjectRoot: "/repo", Held: true, Owner: "roadmap-run:lane-20261103-042422-8f5e"}
	if g, _ := leaseHolderGone(wf, missing, state("")); g != "owner_job_record_missing" {
		t.Fatal(g)
	}
	if g, _ := leaseHolderGone(wf, alive, state("")); g != "" {
		t.Fatal("live job marked gone")
	}
	if g, r := leaseHolderGone(rr, alive, state("stopped")); g != "roadmap_run_stopped" || !strings.Contains(r, "lane-20261103-042422-8f5e") {
		t.Fatal(g, r)
	}
	if g, _ := leaseHolderGone(rr, alive, state("running")); g != "" {
		t.Fatal("running run marked gone")
	}
	base := leaseSchedulingStatus(rr, jobs.ProjectActivity{}, nil, nil)
	if got := markHolderGone(base, rr, jobs.ProjectActivity{}, nil, "roadmap_run_stopped", ""); got.WriterScopeState != "orphaned" || !strings.HasPrefix(got.NextAction, "project_lease_admin_stale_preview") {
		t.Fatalf("%+v", got)
	}
	if got := markHolderGone(base, rr, jobs.ProjectActivity{Count: 1}, nil, "roadmap_run_stopped", ""); got.WriterScopeState == "orphaned" {
		t.Fatal("pending jobs ignored")
	}
	if got := markHolderGone(base, rr, jobs.ProjectActivity{}, &workflow.Run{ID: "x"}, "roadmap_run_stopped", ""); got.WriterScopeState == "orphaned" {
		t.Fatal("live claim ignored")
	}
}
