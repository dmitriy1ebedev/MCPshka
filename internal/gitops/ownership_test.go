package gitops

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// MCP-017: a sibling worktree owned by another user yields a typed refusal
// with the owner and the registration route, never a safe.directory bypass.
func TestStatusForeignOwnedWorktreeGivesTypedRefusal(t *testing.T) {
	dir := t.TempDir()
	stderr := []byte("fatal: detected dubious ownership in repository at '" + dir + "'\nTo add an exception for this directory, call:\n\n\tgit config --global --add safe.directory " + dir + "\n")
	err := commandError("git status", errors.New("exit status 128"), stderr)
	if !errors.Is(err, ErrOwnershipUnverified) {
		t.Fatalf("want typed refusal, got %v", err)
	}
	msg := err.Error()
	uid := strconv.Itoa(os.Geteuid())
	if !strings.Contains(msg, "owned by uid "+uid) || !strings.Contains(msg, "project_repo_adopt path="+dir) || strings.Contains(msg, "--global") {
		t.Fatalf("refusal lacks owner/route or leaks bypass advice: %s", msg)
	}
	plain := commandError("git status", errors.New("exit status 128"), []byte("fatal: not a git repository"))
	if errors.Is(plain, ErrOwnershipUnverified) || !strings.Contains(plain.Error(), "not a git repository") {
		t.Fatalf("other failures unchanged: %v", plain)
	}
}
