package roadmaprun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type failingHeartbeatLease struct{ fakeLease }

func (l *failingHeartbeatLease) Heartbeat(context.Context, string, string, string) error {
	return errors.New("SQLite state budget initialization failed")
}

func TestHeartbeatFailureRetainsLeaseAndWIP(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := withStateDir(t)
		wip := filepath.Join(dir, "work.go")
		original := []byte("uncommitted owner work\n")
		if err := os.WriteFile(wip, original, 0600); err != nil {
			t.Fatal(err)
		}
		lease := &failingHeartbeatLease{}
		calls := 0
		j, err := Run(context.Background(), Config{Agents: []string{"codex", "claude"},
			ID: "heartbeat-failure", Path: dir, Goal: "repair", Budget: time.Hour, MaxPhases: 1,
			Lease: lease,
			Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
				t.Fatal("test admitted after lost heartbeat")
				return "", "", "", nil
			},
			RunAgent: func(ctx context.Context, _, _, _ string) (string, error) {
				calls++
				<-ctx.Done()
				return "", ctx.Err()
			},
		}, "")
		if err != nil || lease.released != 0 || calls != 1 || j.Step != "reconciliation_required" ||
			!strings.Contains(j.NextAction, "project_lease_status") ||
			!strings.Contains(j.Blocker, "heartbeat") {
			t.Fatalf("journal=%+v err=%v released=%d calls=%d", j, err, lease.released, calls)
		}
		got, err := os.ReadFile(wip)
		if err != nil || string(got) != string(original) {
			t.Fatal("WIP changed", err)
		}
	})
}

func TestCancellationRetainsRunnerLease(t *testing.T) {
	withStateDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	lease := &fakeLease{}
	j, err := Run(ctx, Config{Agents: []string{"codex", "claude"},
		ID: "cancel-retain", Goal: "repair", Budget: time.Hour, MaxPhases: 1, Lease: lease,
		Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
			t.Fatal("test after cancel")
			return "", "", "", nil
		},
		RunAgent: func(context.Context, string, string, string) (string, error) { cancel(); return "", context.Canceled },
	}, "")
	if err != nil || lease.released != 0 || j.Step != "reconciliation_required" {
		t.Fatalf("journal=%+v err=%v releases=%d", j, err, lease.released)
	}
}

func TestAttemptDeadlineRetainsLeaseWithoutRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withStateDir(t)
		lease := &fakeLease{}
		calls := 0
		j, err := Run(context.Background(), Config{Agents: []string{"codex", "claude"},
			ID: "attempt-deadline", Goal: "repair", Budget: time.Hour, PhaseMax: time.Second, MaxPhases: 1, Lease: lease,
			Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
				t.Fatal("test after timeout")
				return "", "", "", nil
			},
			RunAgent: func(ctx context.Context, _, _, _ string) (string, error) { calls++; <-ctx.Done(); return "", ctx.Err() },
		}, "")
		if err != nil || calls != 1 || lease.released != 0 || j.Step != "reconciliation_required" {
			t.Fatalf("journal=%+v err=%v calls=%d releases=%d", j, err, calls, lease.released)
		}
	})
}
