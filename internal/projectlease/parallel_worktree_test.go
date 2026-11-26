package projectlease

import (
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIndependentSiblingWorktreeLeases(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "main")
	wt := filepath.Join(base, "worker")
	state := filepath.Join(base, "state")
	os.MkdirAll(state, 0700)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = policy.MinimalEnv(base)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %s", e, out)
		}
	}
	git("init", "-q", root)
	git("-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "--no-gpg-sign", "-m", "fixture")
	git("-C", root, "worktree", "add", "--quiet", "-b", "worker", wt)
	if _, e := statestore.Open(filepath.Join(state, "state.db")); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600)
	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	s := New(filepath.Join(state, "leases"), p)
	a, e := s.AcquireTracked(root, "integrator", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.AcquireTracked(wt, "worker", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if e != nil {
		t.Fatal("independent writer blocked", e)
	}
	if a.Status.ProjectRoot == b.Status.ProjectRoot {
		t.Fatal("worktree scope collapsed")
	}
	if _, e = s.Acquire(root, "competing"); e == nil {
		t.Fatal("two writers in main")
	}
	if _, e = s.Release(root, b.Token); e == nil {
		t.Fatal("foreign token released main")
	}
	if _, e = s.Release(wt, b.Token); e != nil {
		t.Fatal(e)
	}
	if status, e := s.Status(root); e != nil || !status.Held {
		t.Fatal("main lease lost", e)
	}
	if _, e = s.Release(root, a.Token); e != nil {
		t.Fatal(e)
	}
}
