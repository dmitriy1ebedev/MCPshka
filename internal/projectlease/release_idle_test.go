package projectlease

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
)

func TestReleaseIdlePreservesLiveNativeWriter(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(time.Second) })
	m.SetHolderGuard(s.GuardHolder)
	held, err := s.AcquireTracked(root, "owner", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.StartWithOptions(sleep, []string{"1"}, root, jobs.StartOptions{HolderToken: held.Token, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	d := deploy.New(s.policy, filepath.Join(root, "deploys"))
	if _, err := s.ReleaseIdle(root, held.Token, m, d); !failure.IsNoEffect(err) {
		t.Fatal("active writer released or became UNKNOWN", err)
	}
	if _, err := s.ValidateHolder(root, held.Token); err != nil {
		t.Fatal("lost current holder", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := m.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == jobs.Done || j.State == jobs.Failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := s.ReleaseIdle(root, held.Token, m, d); err != nil {
		t.Fatal("idle holder could not clean up", err)
	}
}

func TestReleaseIdleRequiresDeploymentAuthorityForSQLite(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(time.Second) })
	held, err := s.AcquireTracked(root, "owner", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	unknown := deploy.New(s.policy, filepath.Join(t.TempDir(), "deploys"))
	if _, err := s.ReleaseIdle(root, held.Token, m, unknown); !failure.IsNoEffect(err) {
		t.Fatal("unverified deployment permitted release", err)
	}
	if _, err := s.ValidateHolder(root, held.Token); err != nil {
		t.Fatal("unverified deployment lost current holder", err)
	}
	verified := deploy.New(s.policy, filepath.Join(root, "deploys"))
	if _, err := s.ReleaseIdle(root, "incorrect", m, verified); err == nil {
		t.Fatal("incorrect token released holder")
	}
	if _, err := s.ReleaseIdle(root, held.Token, m, verified); err != nil {
		t.Fatal(err)
	}
}
