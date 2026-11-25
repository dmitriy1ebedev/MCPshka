package issues

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShipReleaseAcceptsAncestorFixes(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "old fix")
	old := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "work\n\nFixes MCP-002")
	git("commit", "-q", "--allow-empty", "-m", "0.10.1: release")
	git("commit", "-q", "--allow-empty", "-m", "later\n\nFixes MCP-003")

	s := NewStore(filepath.Join(t.TempDir(), "i.json"))
	for k := 0; k < 3; k++ {
		if _, err := s.Create(NewIssue{Title: fmt.Sprintf("card number %d about thing %d", k, k*7), Priority: "P2", Category: "t", Body: "b"}, "t"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Update("MCP-001", Patch{Status: Fixed, Commit: old[:7]}, "t"); err != nil {
		t.Fatal(err)
	}
	r, err := s.ShipRelease(context.Background(), repo, "0.10.1", func(Issue, string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range r.Accepted {
		got[id] = true
	}
	if !got["MCP-001"] || !got["MCP-002"] || got["MCP-003"] {
		t.Fatalf("accepted %v skipped %v", r.Accepted, r.Skipped)
	}
}
