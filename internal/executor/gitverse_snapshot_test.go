package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIdentityTimeAndSnapshotFormula(t *testing.T) {
	if got := gitIdentityTime("MCPshka Builder <mcpshka@localhost> 1792520608 +0300"); got != "1792520608 +0300" {
		t.Fatal(got)
	}
	for _, bad := range []string{"x", "a <b> notnum +0300", "a <b> 1 0300"} {
		if gitIdentityTime(bad) != "" {
			t.Fatal("accepted", bad)
		}
	}
}

func TestGitVerseSnapshotMatchesPublisher(t *testing.T) {
	repo := t.TempDir()
	run := func(env []string, stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(nil, "", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	run(nil, "", "add", "f")
	run([]string{"GIT_AUTHOR_DATE=2026-10-20T12:00:00+03:00", "GIT_COMMITTER_DATE=2026-10-20T12:05:00+03:00"}, "", "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "c")
	commit := run(nil, "", "rev-parse", "HEAD")
	tree := run(nil, "", "rev-parse", "HEAD^{tree}")
	stamp := strings.Split(run(nil, "", "show", "-s", "--format=%aI%n%cI", commit), "\n")
	env := []string{"GIT_AUTHOR_NAME=MCPshka Snapshot", "GIT_AUTHOR_EMAIL=snapshot@mcpshka.local", "GIT_COMMITTER_NAME=MCPshka Snapshot", "GIT_COMMITTER_EMAIL=snapshot@mcpshka.local", "GIT_AUTHOR_DATE=" + stamp[0], "GIT_COMMITTER_DATE=" + stamp[1]}
	want := run(env, "MCPshka immutable snapshot\n\nSource-Commit: "+commit+"\nSource-Tree: "+tree+"\n", "commit-tree", tree)
	got, err := gitverseSnapshotSHA(context.Background(), repo, commit, tree)
	if err != nil || got != want {
		t.Fatalf("snapshot %s, publisher %s (%v)", got, want, err)
	}
}
