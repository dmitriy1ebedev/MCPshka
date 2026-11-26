package projectlease

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
)

func TestManagedLeaseSurvivesReconnectAndRecoversOnlyGoneOwner(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLeaseGuard(s.Guard)
	id := strings.Repeat("a", 32)
	if _, err := s.AcquireManaged(root, id, m); err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.StartWithOptions(sleep, []string{"1"}, root, jobs.StartOptions{ReservedID: "workflow-" + id})
	if err != nil {
		t.Fatal(err)
	}
	d := deploy.New(s.policy, filepath.Join(root, "deploys"))
	active, err := s.RecoverManaged(root, m, d)
	if err != nil || active.Released || active.Hold != "owner job active" {
		t.Fatalf("live recovery=%+v err=%v", active, err)
	}
	if _, err := m.Start(sleep, []string{"0.1"}, root); err == nil {
		t.Fatal("second writer admitted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, e := m.Get(job.ID)
		if e != nil {
			t.Fatal(e)
		}
		if j.State != jobs.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Loading authority simulates reconnect/restart without trusting the old waiter.
	restarted, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	before, err := restarted.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored := New(filepath.Join(root, "leases"), s.policy)
	recovered, err := restored.RecoverManaged(root, restarted, d)
	if err != nil || !recovered.Released {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	after, _ := restarted.Get(job.ID)
	if after.State != before.State {
		t.Fatal("recovery fabricated job outcome")
	}
	status, err := restored.Status(root)
	if err != nil || status.Held {
		t.Fatal("writer still held", err)
	}
	// A second recovery must not affect a new manual holder.
	next, err := restored.AcquireTracked(root, "interactive-owner", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	again, err := restored.RecoverManaged(root, restarted, d)
	if err != nil || again.Released || again.Hold == "" {
		t.Fatal("legacy/manual holder recovered")
	}
	if _, err := restored.ValidateHolder(root, next.Token); err != nil {
		t.Fatal(err)
	}
}

func TestManagedLeaseHoldsOnDeploymentAndIdentityMismatch(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLeaseGuard(s.Guard)
	id := strings.Repeat("c", 32)
	if _, err := s.AcquireManaged(root, id, m); err != nil {
		t.Fatal(err)
	}
	sleep, _ := exec.LookPath("sleep")
	j, err := m.StartWithOptions(sleep, []string{"0.3"}, root, jobs.StartOptions{ReservedID: "workflow-" + id})
	if err != nil {
		t.Fatal(err)
	}
	d := deploy.New(s.policy, filepath.Join(root, "deploys"))
	if _, err := s.RecoverManaged(root, m, d); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		now, _ := m.Get(j.ID)
		if now.State != jobs.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.database.Put("deploys/1700000000000-0123456789ab.json", []byte(`{"deployment_id":"1700000000000-0123456789ab","state":"accepted"}`)); err != nil {
		t.Fatal(err)
	}
	held, err := s.RecoverManaged(root, m, d)
	if err != nil || held.Released || !strings.Contains(held.Hold, "deployment") {
		t.Fatalf("deployment guard %+v %v", held, err)
	}
	if err := s.database.Put("deploys/1700000000000-0123456789ab.json", []byte(`{"deployment_id":"1700000000000-0123456789ab","state":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, err := s.database.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	r.OwnerStartTicks++
	replacement, _ := json.Marshal(r)
	if err = s.database.Put(key, replacement); err != nil {
		t.Fatal(err)
	}
	mismatch, err := s.RecoverManaged(root, m, d)
	if err != nil || mismatch.Released || !strings.Contains(mismatch.Hold, "identity") {
		t.Fatalf("identity guard %+v %v", mismatch, err)
	}
}
