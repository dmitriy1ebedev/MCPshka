package selfheal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestScopedFreshFailureAfterVerifiedRecoveryIsRateLimited(t *testing.T) {
	f := setup(t)
	b := &fakeTarget{observation: TargetObservation{Owned: true, KnownFailure: true, NativeExhausted: true, ObservedAt: f.now}, verified: true}
	target := Target{Kind: "own_artifact", ID: "index-health", Scope: "searchindex/v1/health", Fingerprint: strings.Repeat("a", 64)}
	for attempt := 0; attempt < 3; attempt++ {
		b.observation.ObservedAt = f.now
		phase, err := f.s.RepairScoped(context.Background(), target, b)
		want := "recovered"
		if attempt == 2 {
			want = "ESCALATED_RATE_LIMIT"
		}
		if err != nil || phase != want {
			t.Fatal(attempt, phase, err)
		}
		f.now = f.now.Add(2 * time.Second)
	}
	if b.calls != 2 {
		t.Fatal("flapping exceeded repair limit", b.calls)
	}
}
