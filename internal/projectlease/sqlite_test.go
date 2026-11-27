package projectlease

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestSQLiteLeaseSurvivesRestartAndRelease(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600)
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	s := New(filepath.Join(root, "leases"), p)
	a, err := s.Acquire(root, "owner")
	if err != nil {
		t.Fatal(err)
	}
	s = New(filepath.Join(root, "leases"), p)
	if got, err := s.Status(root); err != nil || !got.Held {
		t.Fatal("lost lease", err)
	}
	if _, err := s.Acquire(root, "second"); err == nil {
		t.Fatal("duplicate writer")
	}
	if _, err := s.Release(root, "wrong"); err == nil {
		t.Fatal("wrong token")
	}
	if _, err := s.Release(root, a.Token); err != nil {
		t.Fatal(err)
	}
	if keys, err := db.List("leases/"); err != nil || len(keys) != 0 {
		t.Fatal("lease not removed", err)
	}
}

func TestSQLiteReleaseAfterWorktreeRemoval(t *testing.T) {
	service, root := heartbeatSQLiteService(t)
	removed := filepath.Join(root, "worktree")
	if err := os.Mkdir(removed, 0700); err != nil {
		t.Fatal(err)
	}
	lease, err := service.Acquire(removed, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "absent"), removed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(removed, lease.Token); err == nil {
		t.Fatal("released through a dangling symlink")
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(removed, "wrong"); err == nil {
		t.Fatal("released with the wrong holder token")
	}
	manager, err := jobs.New(filepath.Join(root, "jobs"), service.policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(time.Second) })
	unknown := deploy.New(service.policy, filepath.Join(t.TempDir(), "deploys"))
	if _, err := service.ReleaseIdle(removed, lease.Token, manager, unknown); err == nil {
		t.Fatal("released without verified deployment state")
	}
	deployments := deploy.New(service.policy, filepath.Join(root, "deploys"))
	if out, err := service.ReleaseIdle(removed, lease.Token, manager, deployments); err != nil || !out.Released {
		t.Fatalf("release the exact missing scope: %+v, %v", out, err)
	}
	if got, err := service.Status(removed); err != nil || got.Held {
		t.Fatalf("missing scope still held: %+v, %v", got, err)
	}
}

func TestSQLiteHeartbeatAndStaleLeaseRequireReconciliation(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := testService(t, root)
	a, err := s.AcquireTracked(root, "original-owner", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(root, "incorrect-token"); err == nil {
		t.Fatal("non-owner heartbeat accepted")
	}
	renewed, err := s.Heartbeat(root, a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Status.LastHeartbeatAt == nil || renewed.Status.ReconciliationRequired || renewed.Status.Reclaimable {
		t.Fatalf("unexpected heartbeat status: %+v", renewed.Status)
	}
	if renewed.Status.OwnerLiveness != "unverified" {
		t.Fatalf("heartbeat must not claim process liveness: %+v", renewed.Status)
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	before, err := db.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err := json.Unmarshal(before, &r); err != nil {
		t.Fatal(err)
	}
	r.AcquiredAt = time.Now().UTC().Add(-3 * time.Hour)
	r.HeartbeatAt = time.Now().UTC().Add(-2 * time.Hour)
	after, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	after = append(after, '\n')
	if ok, err := db.Replace(key, before, after); err != nil || !ok {
		t.Fatalf("cannot stage aged lease: changed=%v err=%v", ok, err)
	}
	status, err := s.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Held || !status.ReconciliationRequired || status.Reclaimable || status.AgeSeconds < 3600 {
		t.Fatalf("stale lease must remain held and non-reclaimable: %+v", status)
	}
	if _, err := s.Acquire(root, "another-owner"); err == nil {
		t.Fatal("stale lease was auto-reclaimed")
	}
	if _, err := s.Heartbeat(root, a.Token); err != nil {
		t.Fatal("original holder cannot heartbeat stale lease:", err)
	}
	status, err = s.Status(root)
	if err != nil || status.ReconciliationRequired {
		t.Fatalf("owner heartbeat did not clear review signal: %+v err=%v", status, err)
	}
	if _, err := s.Release(root, a.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(root, a.Token); err == nil {
		t.Fatal("heartbeat resurrected released lease")
	}
	next, err := s.Acquire(root, "another-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(root, a.Token); err == nil {
		t.Fatal("previous holder renewed replacement lease")
	}
	if _, err := s.Release(root, next.Token); err != nil {
		t.Fatal(err)
	}
}
