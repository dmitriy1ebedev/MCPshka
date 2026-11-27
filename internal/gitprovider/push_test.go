package gitprovider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git failed (args=%q): %v, output %s", args, err, raw)
	}
}
func TestFullHistoryScanRejectsDeletedSecret(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-b", "main")
	gitFixture(t, dir, "config", "user.name", "Test")
	gitFixture(t, dir, "config", "user.email", "test@example.invalid")
	path := filepath.Join(dir, "private.txt")
	if err := os.WriteFile(path, []byte("ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, dir, "add", "private.txt")
	gitFixture(t, dir, "commit", "-m", "first")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, dir, "add", "-u")
	gitFixture(t, dir, "commit", "-m", "removed")
	err := preflightGitHistory(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "SECRET_PATTERN_DETECTED") {
		t.Fatalf("secret in deleted historical blob not held: %v", err)
	}
	if strings.Contains(err.Error(), "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatal("error contains sensitive bytes")
	}
}
func TestFullHistoryScanBenign(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-b", "main")
	gitFixture(t, dir, "config", "user.name", "Test")
	gitFixture(t, dir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, dir, "add", "main.go")
	gitFixture(t, dir, "commit", "-m", "hello")
	if err := preflightGitHistory(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}
func TestProviderURLAllowlist(t *testing.T) {
	for _, site := range []Site{GitHub, GitVerse, MosHub} {
		u, err := remoteURL(site, "owner", "repo")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(u, "https://") || !strings.HasSuffix(u, "/owner/repo.git") {
			t.Fatalf("invalid remote: %s", u)
		}
		if _, err := remoteURL(site, "owner/evil", "repo"); err == nil {
			t.Fatal("slash owner permitted")
		}
		if _, err := remoteURL(site, "owner", "../evil"); err == nil {
			t.Fatal("traversal repo permitted")
		}
	}
}
