package integrationqueue

import (
	"strings"
	"testing"
)

func failedCandidate(t *testing.T) (*Queue, string) {
	t.Helper()
	q := new(Queue)
	commit := strings.Repeat("a", 40)
	if err := q.Enqueue("p19", "feature/p19", commit); err != nil {
		t.Fatal(err)
	}
	if err := q.Validate("p19", commit); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete("p19", commit, false); err != nil {
		t.Fatal(err)
	}
	return q, commit
}
func TestReconcileRequiresGitEvidence(t *testing.T) {
	q, commit := failedCandidate(t)
	head := strings.Repeat("b", 40)
	for _, e := range []RecoveryEvidence{
		{},
		{TargetHead: head, CandidateReachable: true, CandidateMerged: true},
		{TargetHead: head, CleanTarget: true, CandidateMerged: true},
		{TargetHead: head, CleanTarget: true, CandidateReachable: true},
	} {
		if err := q.ReconcileFailed("p19", commit, e); err == nil {
			t.Fatalf("unsafe recovery: %+v", e)
		}
		if q.Snapshot()[0].State != Failed {
			t.Fatal("changed on invalid evidence")
		}
	}
	if err := q.ReconcileFailed("p19", commit, RecoveryEvidence{TargetHead: head, CleanTarget: true, CandidateReachable: true, CandidateMerged: true}); err != nil {
		t.Fatal(err)
	}
	if q.Snapshot()[0].State != Integrated {
		t.Fatal("merged item not recovered")
	}
}
func TestReconcileCannotChangeCompleted(t *testing.T) {
	q, commit := failedCandidate(t)
	head := strings.Repeat("b", 40)
	e := RecoveryEvidence{TargetHead: head, CleanTarget: true, CandidateReachable: true, CandidateMerged: true}
	if err := q.ReconcileFailed("p19", commit, e); err != nil {
		t.Fatal(err)
	}
	if err := q.ReconcileFailed("p19", commit, e); err == nil {
		t.Fatal("replayed recovery accepted")
	}
}
