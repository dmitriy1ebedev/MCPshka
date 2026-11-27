package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateCopyApprovalPinsSourceAndRejectsChanges(t *testing.T) {
	c, _, owner, source, hash := candidateOwnerFixture(t)
	parent := t.TempDir()
	name := "mcpshka-0.1.99-r1.candidate"
	body, err := os.ReadFile(source.Name())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, name)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	id, candidate := strings.Repeat("8", 32), strings.Repeat("7", 32)
	probe := &candidateStoppedProbe{}
	p, err := c.approveCandidateCopy(context.Background(), parent, id, candidate, name, hash, owner, probe)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, f, err := c.openApprovedCandidate(context.Background(), id, parent)
		if err != nil {
			t.Fatal(err)
		}
		if got != p {
			t.Fatal("approval changed")
		}
		f.Close()
	}
	if _, err := c.approveCandidateCopy(context.Background(), parent, id, candidate, name, hash, owner, probe); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, f, err := c.openApprovedCandidate(context.Background(), id, parent); !errors.Is(err, ErrHold) {
		if f != nil {
			f.Close()
		}
		t.Fatal("changed source accepted", err)
	}
}

func TestCandidateCopyApprovalRejectsCallerPathsAndUnknownOwner(t *testing.T) {
	c, _, owner, source, hash := candidateOwnerFixture(t)
	parent := t.TempDir()
	body, _ := os.ReadFile(source.Name())
	name := "mcpshka-0.1.99.candidate"
	if err := os.WriteFile(filepath.Join(parent, name), body, 0600); err != nil {
		t.Fatal(err)
	}
	id, candidate := strings.Repeat("8", 32), strings.Repeat("7", 32)
	for _, bad := range []string{"../" + name, "/root/secret.candidate", "mcpshka-a\n.candidate", "mcpshka- .candidate", "other.candidate"} {
		if _, err := c.approveCandidateCopy(context.Background(), parent, id, candidate, bad, hash, owner, &candidateStoppedProbe{}); !errors.Is(err, ErrHold) {
			t.Fatalf("path accepted: %q %v", bad, err)
		}
	}
	if _, err := c.approveCandidateCopy(context.Background(), parent, id, candidate, name, hash, owner, &candidateStoppedProbe{deny: true}); !errors.Is(err, ErrHold) {
		t.Fatal("unknown owner accepted", err)
	}
	if _, f, err := c.openApprovedCandidate(context.Background(), id, parent); !errors.Is(err, ErrHold) {
		if f != nil {
			f.Close()
		}
		t.Fatal("unapproved source accepted", err)
	}
}
