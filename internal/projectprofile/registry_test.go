package projectprofile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestRegisteredProfileIsSharedByWorktreesAndOverridesBuiltins(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "lane")
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "tests"), 0755); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "-q", "-b", "main")
	run(repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "init")
	sibling := filepath.Join(base, "lane-wt")
	run(repo, "worktree", "add", "-q", sibling)

	db, err := statestore.Open(filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	SetRegistry(db)
	defer SetRegistry(nil)
	s := New(&policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}})

	proposed, err := s.Propose(repo)
	if err != nil || proposed.Workflows["test"].Command != "python3" {
		t.Fatalf("tests/ directory not proposed as unittest: %+v %v", proposed, err)
	}
	profile := Profile{Schema: SchemaV1, Name: "lane", Workflows: map[string]Workflow{"test": {Command: "sh", Args: []string{"tests/run_safe.sh"}, TimeoutSeconds: 1800, OutputLimitBytes: 1 << 20}}}
	if _, err := s.Register(repo, profile); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{repo, sibling} {
		if err := RecoveryAuthority(path, db); err != nil {
			t.Fatal("registered recovery refused", err)
		}
		got, err := s.Resolve(path)
		if err != nil || got.Source != "registered" || got.Profile.Workflows["test"].Args[0] != "tests/run_safe.sh" {
			t.Fatalf("registered profile not resolved for %s: %+v %v", path, got, err)
		}
	}
	// Replacing keeps one record; an invalid profile is rejected.
	profile.Workflows["test"] = Workflow{Command: "make", Args: []string{"test"}, TimeoutSeconds: 60, OutputLimitBytes: 1024}
	if _, err := s.Register(sibling, profile); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve(repo); got.Profile.Workflows["test"].Command != "make" {
		t.Fatal("replacement not shared", got)
	}
	profile.Workflows["test"] = Workflow{Command: "/bin/sh", TimeoutSeconds: 60, OutputLimitBytes: 1024}
	if _, err := s.Register(repo, profile); err == nil {
		t.Fatal("path command registered")
	}
}
