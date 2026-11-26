package rootbroker

import "testing"

func TestRecoveryHealthDegradesAfterThreeFailedSweeps(t *testing.T) {
	for failed, want := range map[int64]string{0: "ok", 2: "ok", 3: "degraded", 40: "degraded"} {
		if got := recoveryHealth(failed)["state"]; got != want {
			t.Fatalf("failed=%d state=%v want %s", failed, got, want)
		}
	}
}
