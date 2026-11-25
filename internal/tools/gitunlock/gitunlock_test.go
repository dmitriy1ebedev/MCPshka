package gitunlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
)

func TestGitUnlockOnlyStaleLocks(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	leases := projectlease.New(filepath.Join(base, "leases"), p)
	git := gitops.New(p)
	lock := filepath.Join(repo, ".git", "index.lock")
	if out, err := gitUnlock(leases, git, GitUnlockInput{Path: repo}, time.Now()); err != nil || out.Removed {
		t.Fatal("absent lock must be a no-op", out, err)
	}
	if err := os.WriteFile(lock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitUnlock(leases, git, GitUnlockInput{Path: repo}, time.Now()); err == nil {
		t.Fatal("fresh lock removed")
	}
	out, err := gitUnlock(leases, git, GitUnlockInput{Path: repo}, time.Now().Add(time.Hour))
	if err != nil || !out.Removed {
		t.Fatal("stale lock kept", out, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock still present")
	}
}
