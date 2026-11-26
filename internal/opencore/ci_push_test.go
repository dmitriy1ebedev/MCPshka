package opencore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIPushUsesOnlyAuditedTree(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, remote := filepath.Join(dir, "source"), filepath.Join(dir, "public.git")
	git := func(path, input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v", args[0], err)
		}
		return strings.TrimSpace(string(out))
	}
	git(dir, "", "init", repo)
	git(dir, "", "init", "--bare", remote)
	put := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(repo, ConfigPath), `{"exclude":["private.txt"]}`)
	put(filepath.Join(repo, "public.txt"), "public")
	put(filepath.Join(repo, "private.txt"), "private")
	git(repo, "", "add", ".")
	git(repo, "", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.org", "commit", "-qm", "fixture")
	rev := git(repo, "", "rev-parse", "HEAD")
	s, err := StageCI(ctx, repo, rev, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body := "tree " + s.Tree + "\nauthor Fixture <fixture@example.org> 1234567890 +0000\ncommitter Fixture <fixture@example.org> 1234567890 +0000\n\nExport\n"
	sha := git(repo, body, "hash-object", "-t", "commit", "--stdin")
	ref := "refs/heads/ci/" + rev[:12] + "/shard-0"
	target := Target{Site: "github", URL: remote, Auth: "none"}
	put(filepath.Join(s.Dir, "public.txt"), "modified after staging")
	if err := s.PushCI(ctx, target, ref, sha, body); err == nil {
		t.Fatal("modified tree passed its identity check")
	}
	if result := git(repo, "", "ls-remote", "--heads", remote); result != "" {
		t.Fatal("modified export created a ref")
	}
	put(filepath.Join(s.Dir, "public.txt"), "public")
	if err := s.PushCI(ctx, target, ref, sha, body); err != nil {
		t.Fatal(err)
	}
	other := "refs/heads/ci/" + rev[:12] + "/shard-1"
	if err := s.PushCI(ctx, target, other, sha, body); err != nil {
		t.Fatalf("reusing the audited snapshot for a second shard: %v", err)
	}
	if err := target.PruneCI(ctx, other, sha); err != nil {
		t.Fatal(err)
	}
	if result := git(remote, "", "ls-tree", "--name-only", sha); result != "public.txt" {
		t.Fatal("unexpected public files", result)
	}
	if result := git(remote, "", "rev-list", "--parents", sha); result != sha {
		t.Fatal("public snapshot has a parent")
	}
	if err := target.PruneCI(ctx, ref, sha); err != nil {
		t.Fatal(err)
	}
	if result := git(repo, "", "ls-remote", "--heads", remote); result != "" {
		t.Fatal("pruned ref remains")
	}
}
