package projectlease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func heartbeatSQLiteService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	_, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	return New(filepath.Join(root, "leases"), p), root
}

func TestTrackedLeaseHeartbeatSurvivesRestart(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	id := strings.Repeat("a", 32)
	acquired, err := s.AcquireTracked(root, "remote-chat", id)
	if err != nil {
		t.Fatal(err)
	}
	initial := acquired.Status
	if initial.ExecutionID != id || initial.HeartbeatAt.IsZero() {
		t.Fatalf("missing tracked identity: %+v", initial)
	}
	if _, err := s.HeartbeatRefresh(root, "wrong"); err == nil {
		t.Fatal("incorrect token accepted")
	}
	refreshed, err := s.HeartbeatRefresh(root, acquired.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.HeartbeatAt.After(initial.HeartbeatAt) {
		t.Fatalf("heartbeat did not advance: %+v", refreshed)
	}
	if refreshed.AcquiredAt != initial.AcquiredAt || refreshed.ExecutionID != id {
		t.Fatal("lease identity changed")
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	s = New(filepath.Join(root, "leases"), p)
	persisted, err := s.Status(root)
	if err != nil || persisted.HeartbeatAt != refreshed.HeartbeatAt {
		t.Fatalf("heartbeat not durable: %+v %v", persisted, err)
	}
	if _, err := s.AcquireTracked(root, "another", strings.Repeat("b", 32)); err == nil {
		t.Fatal("active writer displaced")
	}
	if _, err := s.Release(root, acquired.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HeartbeatRefresh(root, acquired.Token); err == nil {
		t.Fatal("renewed deleted lease")
	}
}

func TestLegacyLeaseCannotAutoEnrollIntoRecovery(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	legacy, err := s.Acquire(root, "legacy-writer")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Status.ExecutionID != "" {
		t.Fatalf("legacy should be untracked: %+v", legacy.Status)
	}
	if _, err := s.HeartbeatRefresh(root, legacy.Token); err == nil {
		t.Fatal("legacy lease silently upgraded")
	}
	status, err := s.Status(root)
	if err != nil || !status.Held || status.Owner != "legacy-writer" {
		t.Fatalf("legacy changed: %+v %v", status, err)
	}
	if _, err := s.Release(root, legacy.Token); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseExecutionIDFailClosed(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	for _, id := range []string{"", strings.Repeat("X", 32), "../../release", strings.Repeat("a", 31), strings.Repeat("f", 33)} {
		if id == "" {
			continue
		}
		if _, err := s.AcquireTracked(root, "writer", id); err == nil {
			t.Fatalf("invalid execution id accepted: %q", id)
		}
	}
	if status, err := s.Status(root); err != nil || status.Held {
		t.Fatalf("invalid ID acquired lease %+v %v", status, err)
	}
	if !validExecutionID(strings.Repeat("a", 32)) {
		t.Fatal("valid ID rejected")
	}
}

func TestHeartbeatCannotRenewFileFallback(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	s := New(filepath.Join(root, "leases"), p)
	a, err := s.AcquireTracked(root, "legacy-fallback", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.HeartbeatRefresh(root, a.Token); err == nil {
		t.Fatal("file fallback heartbeat requires SQLite CAS")
	}
	// Verify the failed renewal did not remove or change the writer.
	got, err := s.Status(root)
	if err != nil || !got.Held || got.Owner != "legacy-fallback" {
		t.Fatalf("fallback unsafe: %+v %v", got, err)
	}
	if _, err := s.Release(root, a.Token); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatTimeAlwaysAdvances(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	a, err := s.AcquireTracked(root, "writer", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	last := a.Status.HeartbeatAt
	for i := 0; i < 4; i++ {
		next, err := s.HeartbeatRefresh(root, a.Token)
		if err != nil {
			t.Fatal(err)
		}
		if !next.HeartbeatAt.After(last) {
			t.Fatalf("not monotonic at %d", i)
		}
		if time.Since(next.HeartbeatAt) > time.Minute {
			t.Fatal("timestamp not current")
		}
		last = next.HeartbeatAt
	}
}
