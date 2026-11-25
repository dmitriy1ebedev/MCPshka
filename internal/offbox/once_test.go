package offbox

import (
	"context"
	"strings"
	"testing"

	"mcpshka/internal/runnerhub"
)

// S5.DIAGCI: a second off-box test of the same commit that nobody serves is
// not run on H96 again; it names the earlier local job.
func TestOneLocalFallbackPerCommit(t *testing.T) {
	d, _, j := testDriver(t)
	calls := 0
	d.step(context.Background(), j.ID, localStub(&calls))
	again := j
	again.ID, again.KeyHash = runnerhub.NewJobID(), ""
	again, err := d.Hub.Enqueue(again)
	if err != nil {
		t.Fatal(err)
	}
	d.step(context.Background(), again.ID, localStub(&calls))
	if calls != 1 {
		t.Fatalf("H96 ran the commit %d times", calls)
	}
	got, _ := d.Hub.Job(again.ID)
	if got.Fallback != "" || !strings.Contains(got.Error, "h96-job") {
		t.Fatalf("second job: %+v", got)
	}
	other := j
	other.ID, other.KeyHash, other.Commit = runnerhub.NewJobID(), "", strings.Repeat("b", 40)
	if other, err = d.Hub.Enqueue(other); err != nil {
		t.Fatal(err)
	}
	d.step(context.Background(), other.ID, localStub(&calls))
	if calls != 2 {
		t.Fatal("a new commit must still fall back to H96")
	}
}
