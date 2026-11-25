package salvage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Fixture repositories must not inherit executable filters or other host Git policy.
func TestMain(m *testing.M) {
	if err := os.Setenv("GIT_CONFIG_NOSYSTEM", "1"); err != nil {
		panic(err)
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitFile(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", msg)
}

// MCP-054: a cherry-picked fix is superseded (no repeat pick), a unique fix
// is not_released, a merged branch is integrated and a dirty worktree is WIP.
func TestPreviewSeparatesSupersededPendingAndWIP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "a", "1\n", "base")

	git(t, repo, "checkout", "-q", "-b", "codex/picked")
	commitFile(t, repo, "b", "fix\n", "fix b")
	git(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "m", "main moved\n", "main moves on") // else the pick is the identical commit
	git(t, repo, "cherry-pick", "codex/picked")

	git(t, repo, "checkout", "-q", "-b", "claude/unique")
	commitFile(t, repo, "c", "new\n", "feature c")
	git(t, repo, "checkout", "-q", "main")
	git(t, repo, "branch", "codex/merged", "main")

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "agent/wip", wt, "main")
	commitFile(t, wt, "d", "x\n", "wip base")
	if err := os.WriteFile(filepath.Join(wt, "d"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rep, err := Preview(context.Background(), repo, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Candidate{}
	for _, c := range rep.Candidates {
		got[c.Branch] = c
	}
	want := map[string]string{"codex/picked": Superseded, "claude/unique": NotReleased, "codex/merged": Integrated, "agent/wip": WIP}
	for b, s := range want {
		if got[b].State != s {
			t.Errorf("%s: state %q want %q (%+v)", b, got[b].State, s, got[b])
		}
	}
	if c := got["claude/unique"]; c.Pending != 1 || len(c.Commits) != 1 || c.Commits[0].Equivalent {
		t.Fatalf("unique: %+v", c)
	}
	if c := got["codex/picked"]; c.Pending != 0 || len(c.Commits) != 1 || !c.Commits[0].Equivalent {
		t.Fatalf("picked: %+v", c)
	}
	if _, err := Preview(context.Background(), repo, "main;rm", nil); err == nil {
		t.Fatal("unsafe base accepted")
	}
}
