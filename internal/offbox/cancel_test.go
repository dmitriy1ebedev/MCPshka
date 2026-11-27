package offbox

import (
	"context"
	"testing"

	"mcpshka/internal/jobs"
)

type cancelLocal struct {
	localJobs
	cancelled *string
}

func (c cancelLocal) Cancel(id string) (jobs.Snapshot, error) {
	*c.cancelled = id
	return jobs.Snapshot{ID: id, State: jobs.Cancelling}, nil
}

// MCP-292: job_cancel on a queued rj- job stops it; the driver does not
// wake clouds or fall back to H96 afterwards.
func TestCancelQueuedRunnerJob(t *testing.T) {
	d, _, j := testDriver(t)
	s, err := View{Hub: d.Hub}.Cancel(j.ID)
	if err != nil || s.State != jobs.Cancelled {
		t.Fatalf("%+v %v", s, err)
	}
	calls := 0
	if !d.step(context.Background(), j.ID, localStub(&calls)) || calls != 0 {
		t.Fatal("a cancelled job must not fall back to H96")
	}
}

// A leased job is released: the runner's next heartbeat says stop.
func TestCancelLeasedRunnerJobStopsRunner(t *testing.T) {
	d, _, j := testDriver(t)
	tok, _, _ := d.Hub.MintEphemeral("github")
	rid, _, err := d.Hub.Register(tok, "ci-github-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.Hub.Poll(rid, map[string]string{"go": "1"}, 0); err != nil || got == nil {
		t.Fatal("poll", err)
	}
	if _, err := (View{Hub: d.Hub}).Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	if stop, err := d.Hub.Heartbeat(rid, j.ID); err != nil || !stop {
		t.Fatalf("heartbeat stop=%v err=%v", stop, err)
	}
	if err := d.Hub.Result(rid, j.ID, 0, "late", ""); err == nil {
		t.Fatal("a cancelled job must not take a late result")
	}
}

// After a fallback job_cancel reaches the H96 job.
func TestCancelFollowsFallback(t *testing.T) {
	d, _, j := testDriver(t)
	calls := 0
	d.step(context.Background(), j.ID, localStub(&calls))
	got := ""
	s, err := View{Hub: d.Hub, Local: cancelLocal{cancelled: &got}}.Cancel(j.ID)
	if err != nil || got != "h96-job" || s.State != jobs.Cancelling {
		t.Fatalf("%+v %v cancelled=%q", s, err, got)
	}
}

// Unknown rj- id is not found, not a panic.
func TestCancelUnknownRunnerJob(t *testing.T) {
	d, _, _ := testDriver(t)
	if _, err := (View{Hub: d.Hub}).Cancel("rj-0000000000000000"); err != jobs.ErrNotFound {
		t.Fatal(err)
	}
}
