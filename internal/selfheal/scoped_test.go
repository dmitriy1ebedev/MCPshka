package selfheal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeTarget struct {
	uncertain   bool
	calls       int
	observation TargetObservation
	verified    bool
}

func (f *fakeTarget) Observe(context.Context, Target) (TargetObservation, error) {
	return f.observation, nil
}
func (f *fakeTarget) Repair(context.Context, Target, string) (string, error) {
	f.calls++
	if f.uncertain {
		return "", errors.New("lost reply")
	}
	return "receipt", nil
}
func (f *fakeTarget) Verify(context.Context, Target, string) (bool, error)   { return f.verified, nil }
func (f *fakeTarget) Rollback(context.Context, Target, string) (bool, error) { return true, nil }
func TestScopedRepairNativeFirstAndNoReplay(t *testing.T) {
	for _, kind := range []string{"stale_job", "own_artifact", "failed_unit"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			b := &fakeTarget{observation: TargetObservation{Owned: true, KnownFailure: true, NativeBusy: true, ObservedAt: f.now}, verified: true}
			target := Target{kind, "mcpshka-job-fixture.service", "own-scope", strings.Repeat("a", 64)}
			phase, err := f.s.RepairScoped(context.Background(), target, b)
			if err != nil || phase != "WAIT_NATIVE" || b.calls != 0 {
				t.Fatal(phase, err)
			}
			b.observation.NativeBusy = false
			b.observation.NativeExhausted = true
			phase, err = f.s.RepairScoped(context.Background(), target, b)
			if err != nil || phase != "recovered" || b.calls != 1 {
				t.Fatal(phase, err)
			}
			f.reopen(t)
			phase, err = f.s.RepairScoped(context.Background(), target, b)
			if err != nil || phase != "recovered" || b.calls != 1 {
				t.Fatal("replay", phase, err)
			}
			b.observation.ObservedAt = f.now.Add(-2 * time.Minute)
			if phase, _ := f.s.RepairScoped(context.Background(), target, b); phase != "HOLD" {
				t.Fatal(phase)
			}
		})
	}
}

func TestScopedUnknownCannotCreateReplacement(t *testing.T) {
	f := setup(t)
	b := &fakeTarget{uncertain: true, observation: TargetObservation{Owned: true, KnownFailure: true, NativeExhausted: true, ObservedAt: f.now}}
	target := Target{"stale_job", "original-job", "own-scope", strings.Repeat("a", 64)}
	if phase, err := f.s.RepairScoped(context.Background(), target, b); err != nil || phase != "UNKNOWN" {
		t.Fatal(phase, err)
	}
	target.Fingerprint = strings.Repeat("b", 64)
	if phase, err := f.s.RepairScoped(context.Background(), target, b); err != nil || phase != "UNKNOWN" || b.calls != 1 {
		t.Fatal(phase, err, b.calls)
	}
	f.s.config.Enabled = false
	if phase, _ := f.s.RepairScoped(context.Background(), target, b); phase != "DISABLED" {
		t.Fatal(phase)
	}
}
