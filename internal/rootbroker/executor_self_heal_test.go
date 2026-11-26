package rootbroker

import (
	"testing"
	"time"
)

func TestExecutorSelfHealIsFixedAndSpaced(t *testing.T) {
	for _, a := range [][]string{nil, {"reconcile-stale-permits"}, {selfHealStalePermits, "x"}} {
		if _, err := runExecutorSelfHeal(t.Context(), a); err == nil {
			t.Fatalf("%v accepted", a)
		}
	}
	if err := validateLegacyLeaseRequest([]string{selfHealStalePermits}); err != nil {
		t.Fatal(err)
	}
	selfHealGate.last = time.Time{}
	now := time.Now()
	if err := selfHealAdmit(now); err != nil {
		t.Fatal(err)
	}
	if err := selfHealAdmit(now.Add(time.Minute)); err == nil {
		t.Fatal("second pass within the interval admitted")
	}
	if err := selfHealAdmit(now.Add(selfHealStalePermitsEvery + time.Second)); err != nil {
		t.Fatal(err)
	}
}
