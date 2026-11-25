package mcpserver

import (
	"mcpshka/internal/forgeci"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForgeCIRerunRequiresKnownSiteCommitAndRetryKey(t *testing.T) {
	in := ForgeCIInput{Provider: "gitverse", Mode: "rerun", Commit: strings.Repeat("a", 40), IdempotencyKey: "retry"}
	args, _, err := forgeJobInput(in)
	if err != nil || len(args) != 4 || args[2] != "rerun" {
		t.Fatal(args, err)
	}
	for _, bad := range []ForgeCIInput{
		{Provider: "github", Mode: in.Mode, Commit: in.Commit, IdempotencyKey: in.IdempotencyKey},
		{Provider: in.Provider, Mode: in.Mode, Commit: "HEAD", IdempotencyKey: in.IdempotencyKey},
		{Provider: in.Provider, Mode: in.Mode, Commit: in.Commit},
	} {
		if _, _, err := forgeJobInput(bad); err == nil {
			t.Fatal("unsafe rerun admitted", bad)
		}
	}
}

func TestForgeCIPublishBudgetCoversNetworkRetries(t *testing.T) {
	for _, mode := range []string{"publish", "status", "plan", "mirror", "rerun"} {
		_, opts, err := forgeJobInput(ForgeCIInput{Provider: "gitverse", Mode: mode, Commit: strings.Repeat("a", 40), IdempotencyKey: "retry"})
		if err != nil {
			t.Fatal(err)
		}
		if mode == "publish" {
			if opts.Timeout <= forgeci.PublishTimeout {
				t.Fatal("job truncates retry budget", opts.Timeout)
			}
		} else if opts.Timeout != 120*time.Second {
			t.Fatal("unrelated job budget changed", mode, opts.Timeout)
		}
	}
}

func TestForgeCIUsesHolderTokenWithoutPassingItToSubprocess(t *testing.T) {
	for _, token := range []string{"", "current-holder-secret"} {
		for _, mode := range []string{"publish", "status", "plan", "mirror", "rerun"} {
			in := ForgeCIInput{Provider: "gitverse", Mode: mode, Commit: strings.Repeat("a", 40), IdempotencyKey: "retry", LeaseToken: token}
			args, opts, err := forgeJobInput(in)
			if err != nil {
				t.Fatal(err)
			}
			if opts.HolderToken != token {
				t.Fatal("holder identity was not forwarded to job admission")
			}
			if token != "" && strings.Contains(strings.Join(args, " "), token) {
				t.Fatal("holder secret exposed in subprocess arguments")
			}
		}
	}
}

func TestForgeCIImmutablyPublishesFromOwnedSiblingWithoutRootWriter(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mcpshka")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		argv := append([]string{"-C", dir}, args...)
		out, err := exec.Command("/usr/bin/git", argv...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-q")
	git(root, "config", "user.name", "CI test")
	git(root, "config", "user.email", "ci@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	git(root, "add", "README")
	git(root, "commit", "-qm", "fixture")
	commit := git(root, "rev-parse", "HEAD")
	sibling := filepath.Join(base, "mcpshka-release-worktree")
	git(root, "worktree", "add", "-q", "-b", "release/ci", sibling)
	in := ForgeCIInput{Provider: "github", Mode: "publish", Commit: commit, LeaseToken: "own-sibling-holder", Workdir: sibling, IdempotencyKey: "one"}
	if got, err := forgeCIWorkdir(in, root); err != nil || got != sibling {
		t.Fatalf("verified sibling blocked: %q, %v", got, err)
	}
	for _, mode := range []string{"plan", "status"} {
		in.Mode = mode
		if got, err := forgeCIWorkdir(in, root); err != nil || got != sibling {
			t.Fatalf("immutable %s blocked: %q, %v", mode, got, err)
		}
	}
	for _, mode := range []string{"mirror", "pr", "rerun"} {
		in.Mode = mode
		if _, err := forgeCIWorkdir(in, root); err == nil {
			t.Fatalf("%s bypassed root writer", mode)
		}
	}
	in.Mode = "publish"
	in.LeaseToken = ""
	if _, err := forgeCIWorkdir(in, root); err == nil {
		t.Fatal("unowned sibling accepted")
	}
	in.LeaseToken = "own-sibling-holder"
	in.Commit = strings.Repeat("a", 40)
	if _, err := forgeCIWorkdir(in, root); err == nil {
		t.Fatal("sibling HEAD not pinned")
	}
	in.Commit = commit
	other := filepath.Join(base, "other-repository")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	git(other, "init", "-q")
	in.Workdir = other
	if _, err := forgeCIWorkdir(in, root); err == nil {
		t.Fatal("unrelated repository was trusted as a sibling")
	}
	link := filepath.Join(base, "mcpshka-link")
	if err := os.Symlink(sibling, link); err != nil {
		t.Fatal(err)
	}
	in.Workdir = link
	if _, err := forgeCIWorkdir(in, root); err == nil {
		t.Fatal("symlink source worktree accepted")
	}
}
