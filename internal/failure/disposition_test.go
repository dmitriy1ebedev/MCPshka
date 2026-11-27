package failure

import "testing"

func TestRecoveryDispositions(t *testing.T) {
	for class, want := range map[Class]string{OwnerBusy: "blocker", FailedVerified: "blocker", PolicyDenied: "no_authority", AuthRequired: "needs_approval", ApprovalRequired: "needs_approval", Transient: "transient", Deadline: "transient", FencedUnknown: "blocker", EvidenceMissing: "blocker"} {
		if got := Disposition(class); got != want {
			t.Errorf("%s: %s", class, got)
		}
	}
}
