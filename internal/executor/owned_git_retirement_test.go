package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/integrationqueue"
)

func ownedGitRetirementFixture(t *testing.T) (*Controller, string, IntegrationApproval) {
	t.Helper()
	c, parent, repo, owner := ownedGitBranchFixture(t)
	ctx := context.Background()
	id := strings.Repeat("7", 32)
	if _, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); err != nil {
		t.Fatal(err)
	}
	r := integrationqueue.Request{OwnedBranchID: id, Branch: "task-r1", Commit: owner.PermitCommit, TargetBranch: "main"}
	a := IntegrationApproval{ID: strings.Repeat("8", 64), Request: r, Repository: repo, TestPin: owner.Pin, LaunchID: owner.LaunchID, ExecutionID: strings.Repeat("9", 32)}
	if err := c.reserveOwnedBranchApproval(ctx, id, a.ID); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := publishProtectedRef(ctx, root, "refs/heads/main", owner.PermitCommit, true); err != nil {
		t.Fatal(err)
	}
	return c, parent, a
}

func TestOwnedGitRetirementRecoveryRefReplayAndCrash(t *testing.T) {
	c, parent, a := ownedGitRetirementFixture(t)
	ctx := context.Background()
	crash := errors.New("crash after unlink")
	receipt, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{}, func() error { return nil }, func() error { return crash })
	if !errors.Is(err, crash) || receipt.State != "DELETE_INTENT" {
		t.Fatal(receipt, err)
	}
	if _, err := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/task-r1")); !os.IsNotExist(err) {
		t.Fatal("source not retired", err)
	}
	// The exact recovery inode and absence reconcile a lost reply without
	// requiring a new live-owner observation or repeating a destructive effect.
	receipt, err = c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{deny: true}, func() error { return ErrHold }, nil)
	if err != nil || receipt.State != "DELETED" {
		t.Fatal(receipt, err)
	}
	again, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{deny: true}, func() error { return ErrHold }, func() error { t.Fatal("delete repeated"); return nil })
	if err != nil || again != receipt {
		t.Fatal(again, err)
	}
	root, err := os.OpenRoot(a.Repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	commit, err := readProtectedDirectRef(root, "refs/mcpshka/recovery/owned-branches/"+a.Request.OwnedBranchID)
	if err != nil || commit != a.Request.Commit {
		t.Fatal("recovery commit lost", commit, err)
	}
}

func TestOwnedGitRetirementHoldsUnverifiedWorkAndChangedRef(t *testing.T) {
	for _, kind := range []string{"active-or-unknown", "owner-fence", "replacement", "unintegrated"} {
		t.Run(kind, func(t *testing.T) {
			c, parent, a := ownedGitRetirementFixture(t)
			ctx := context.Background()
			proof := func() error { return nil }
			probe := &candidateStoppedProbe{}
			path := filepath.Join(a.Repository.CommonDir, "refs/heads/task-r1")
			switch kind {
			case "active-or-unknown":
				proof = func() error { return ErrHold }
			case "owner-fence":
				probe.deny = true
			case "replacement":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(a.Request.Commit+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unintegrated":
				if err := os.Remove(filepath.Join(a.Repository.CommonDir, "refs/heads/main")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, probe, proof, nil); !errors.Is(err, ErrHold) {
				t.Fatal("unsafe retirement", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("held source removed", err)
			}
		})
	}
}

func TestOwnedGitRetirementSecondProofBlocksUnlinkAndReplacementReplay(t *testing.T) {
	c, parent, a := ownedGitRetirementFixture(t)
	ctx := context.Background()
	calls := 0
	proof := func() error {
		calls++
		if calls == 2 {
			return ErrHold
		}
		return nil
	}
	if _, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{}, proof, nil); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	path := filepath.Join(a.Repository.CommonDir, "refs/heads/task-r1")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(a.Request.Commit+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{}, func() error { return nil }, nil); !errors.Is(err, ErrHold) {
		t.Fatal("journal adopted replacement", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("replacement removed", err)
	}
}

func TestOwnedGitRetirementReplayRequiresRestorableObjectGraph(t *testing.T) {
	c, parent, a := ownedGitRetirementFixture(t)
	ctx := context.Background()
	if _, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{}, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(a.Repository.CommonDir, "objects", a.Request.Commit[:2], a.Request.Commit[2:])
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	if _, err := c.retireOwnedGitBranchFiles(ctx, parent, uint32(os.Geteuid()), a, 2, &candidateStoppedProbe{}, func() error { return nil }, nil); !errors.Is(err, ErrHold) {
		t.Fatal("unrestorable recovery reported successful", err)
	}
}
