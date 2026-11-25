package mcpserver

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCandidateCleanupAdmissionCanonicalJobAndLease(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	m, err := jobs.New(filepath.Join(t.TempDir(), "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	leases := projectlease.New(t.TempDir(), p)
	alternate := root + "/./"
	job, err := m.Start("sleep", []string{"2"}, root)
	if err != nil {
		t.Fatal(err)
	}
	// Close waits for the job's final persist; Cancel alone races TempDir cleanup.
	t.Cleanup(func() { m.Cancel(job.ID); m.Close(5 * time.Second) })
	called := false
	if err := candidateCleanupAdmission(m, leases, alternate, func() error { called = true; return nil }); err == nil || !strings.Contains(err.Error(), "job still pending") || called {
		t.Fatalf("canonical job bypass: %v called=%v", err, called)
	}
	other := t.TempDir()
	p.AllowedWorkdirs = append(p.AllowedWorkdirs, other)
	held, err := leases.Acquire(other, "owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { leases.Release(other, held.Token) })
	if err := candidateCleanupAdmission(m, leases, other+"/./", func() error { called = true; return nil }); err == nil || !strings.Contains(err.Error(), "lease held") || called {
		t.Fatalf("lease bypass: %v called=%v", err, called)
	}
}
