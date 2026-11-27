package gitops

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

func TestMCP217VerifiedCommitChecksProspectiveIndex(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	digest := func(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }

	git("init", "-q")
	git("config", "core.autocrlf", "false")
	write("selected.txt", "base\n")
	write("removed.txt", "old\n")
	write("unrelated.txt", "old\n")
	write(".gitattributes", "selected.txt text\n")
	git("add", "-A")
	git("commit", "-qm", "base")
	write("unrelated.txt", "staged separately\n")
	git("add", "unrelated.txt")
	before := git("rev-parse", "HEAD")
	index := git("write-tree")
	write("selected.txt", "reviewed\r\n")
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}})
	_, err := s.CommitVerified(root, []string{"selected.txt"}, "reject changed blob", map[string]string{
		"selected.txt": digest("reviewed\r\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "CONCURRENT_WRITER_CONFLICT") {
		t.Fatalf("accepted bytes changed by Git staging: %v", err)
	}
	if git("rev-parse", "HEAD") != before || git("write-tree") != index {
		t.Fatal("rejected tracked-file commit changed HEAD or the index")
	}

	write("selected.txt", "reviewed\n")
	write("it's new.txt", "new bytes\n")
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	files := []string{"selected.txt", "removed.txt", "it's new.txt"}
	if _, err = s.CommitVerified(root, files, "commit verified selection", map[string]string{
		"selected.txt": digest("reviewed\n"),
		"removed.txt":  "DELETED",
		"it's new.txt": digest("new bytes\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := git("diff", "HEAD^", "HEAD", "--name-only"); got != "it's new.txt\nremoved.txt\nselected.txt" {
		t.Fatalf("wrong files committed: %q", got)
	}
	if got := git("diff", "--cached", "--name-only"); got != "unrelated.txt" {
		t.Fatalf("unrelated staging changed: %q", got)
	}
}
