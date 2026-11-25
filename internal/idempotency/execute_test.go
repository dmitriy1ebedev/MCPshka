package idempotency

import (
	"strings"
	"testing"
)

type testUnknownOutcome struct{}

func (testUnknownOutcome) Error() string        { return "unknown" }
func (testUnknownOutcome) UnknownOutcome() bool { return true }

func TestRunIdempotentKeepsUnknownOutcomePending(t *testing.T) {
	store := New(t.TempDir())
	key := "unknown-key"
	_, err := Run(store, key, "network_stage_apply", struct{ Plan string }{"plan-a"}, func() (string, error) {
		return "", testUnknownOutcome{}
	})
	if err == nil {
		t.Fatal("expected unknown outcome error")
	}
	status, err := store.Lookup(key)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != Pending {
		t.Fatalf("state=%s want pending", status.State)
	}
	_, err = Run(store, key, "network_stage_apply", struct{ Plan string }{"plan-a"}, func() (string, error) {
		return "replayed", nil
	})
	if err == nil || !strings.Contains(err.Error(), "PENDING") {
		t.Fatalf("pending unknown outcome was replayed or misclassified: %v", err)
	}
}
