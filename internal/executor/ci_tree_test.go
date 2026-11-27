package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ciTreeRepo builds main -> {github merge of the same change, candidate}.
func ciTreeRepo(t *testing.T) (repo string, git func(...string) string, write func(string, string)) {
	t.Helper()
	repo = t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write = func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "CI Tree")
	git("config", "user.email", "ci@example.invalid")
	write("a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "root")
	return repo, git, write
}

func TestCITestedTreeMatchesOnlyTheCommitTree(t *testing.T) {
	repo, git, write := ciTreeRepo(t)
	root := git("rev-parse", "HEAD")
	ctx := context.Background()
	// Local lineage integrates a change as one commit; the candidate builds on it.
	write("b.txt", "b\n")
	git("add", ".")
	git("commit", "-m", "local integration")
	write("c.txt", "c\n")
	git("add", ".")
	git("commit", "-m", "candidate")
	candidate := git("rev-parse", "HEAD")
	tree := git("rev-parse", "HEAD^{tree}")
	if err := ciTestedTreeMatches(ctx, repo, root, candidate, tree); err != nil {
		t.Fatal("ancestor base rejected", err)
	}
	// GitHub merged the same change through its own commit: diverged, same content.
	git("checkout", "-q", "-b", "github", root)
	write("b.txt", "b\n")
	git("add", ".")
	git("commit", "-m", "github merge of the same change")
	same := git("rev-parse", "HEAD")
	if err := ciTestedTreeMatches(ctx, repo, same, candidate, tree); err != nil {
		t.Fatal("equivalent diverged base rejected", err)
	}
	// A diverged base that adds code means CI tested more than the commit.
	write("d.txt", "only on github\n")
	git("add", ".")
	git("commit", "-m", "extra github change")
	extra := git("rev-parse", "HEAD")
	if err := ciTestedTreeMatches(ctx, repo, extra, candidate, tree); err == nil || !strings.Contains(err.Error(), "not the commit tree") {
		t.Fatal("base with extra changes accepted", err)
	}
	// Conflicting base.
	git("checkout", "-q", "-b", "conflict", root)
	write("b.txt", "conflicting\n")
	git("add", ".")
	git("commit", "-m", "conflict")
	if err := ciTestedTreeMatches(ctx, repo, git("rev-parse", "HEAD"), candidate, tree); err == nil {
		t.Fatal("conflicting base accepted")
	}
	if err := ciTestedTreeMatches(ctx, repo, strings.Repeat("9", 40), candidate, tree); err == nil || !strings.Contains(err.Error(), "fetch the GitHub remote") {
		t.Fatal("missing base not reported", err)
	}
	// The private object directory never leaks merge objects into the repo.
	if out, _ := exec.Command("git", "-C", repo, "count-objects").Output(); strings.Contains(string(out), "error") {
		t.Fatal(string(out))
	}
}
