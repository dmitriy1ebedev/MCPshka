package projectci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// Owner 14.11: «только код» = a parentless snapshot with code and the files
// tests need; the same commit always gives the same snapshot SHA.
func TestCodeOnlySnapshot(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	for f, body := range map[string]string{
		"README.md": "about", "CHANGELOG.md": "c", "LICENSE": "l", "docs/HOWTO.md": "h", "docs/POLICY.md": "kept",
		"main.go": "package main", "tests/run.sh": "x", ".gitverse/workflows/ci.yml": "on: push", "plans/p.txt": "p",
		PublishFile: `{"mode":"code_only","keep":["docs/POLICY.md"],"exclude":["plans/"]}`,
	} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "second")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	c := New()
	snap, cfg, err := c.siteCommit(context.Background(), dir, head)
	if err != nil || cfg.Mode != "code_only" || snap == head {
		t.Fatalf("%s %+v %v", snap, cfg, err)
	}
	files := gitRun(t, dir, "ls-tree", "-r", "--name-only", snap)
	if files != ".gitverse/workflows/ci.yml\n"+PublishFile+"\ndocs/POLICY.md\nmain.go\ntests/run.sh" {
		t.Fatalf("snapshot tree:\n%s", files)
	}
	if parents := gitRun(t, dir, "show", "-s", "--format=%P", snap); parents != "" {
		t.Fatalf("snapshot must carry no history: %q", parents)
	}
	if again, _, _ := c.siteCommit(context.Background(), dir, head); again != snap {
		t.Fatal("snapshot SHA not deterministic: status could not find the run")
	}
	if st := gitRun(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree or index touched: %q", st)
	}
	os.Remove(filepath.Join(dir, PublishFile))
	gitRun(t, dir, "commit", "-q", "-am", "full again")
	full := gitRun(t, dir, "rev-parse", "HEAD")
	if got, cfg, err := c.siteCommit(context.Background(), dir, full); err != nil || got != full || cfg.Mode != "full" {
		t.Fatalf("without publish.json the site gets the commit itself: %s %+v %v", got, cfg, err)
	}
}
