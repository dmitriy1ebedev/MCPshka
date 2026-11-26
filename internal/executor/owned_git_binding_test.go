package executor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"mcpshka/internal/integrationqueue"
)

func TestOwnedGitBranchApprovalBindsOwnerRepositoryRefAndCommit(t *testing.T) {
	c, parent, repo, owner := ownedGitBranchFixture(t)
	id := strings.Repeat("5", 32)
	ctx := context.Background()
	if _, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); err != nil {
		t.Fatal(err)
	}
	r := integrationqueue.Request{OwnedBranchID: id, Branch: "task-r1", Commit: owner.PermitCommit}
	if err := c.validateOwnedBranchBinding(ctx, parent, uint32(os.Geteuid()), r, repo, owner.Pin, owner.LaunchID, &candidateStoppedProbe{}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"resource", "branch", "commit", "repository", "owner", "launch", "fence"} {
		t.Run(change, func(t *testing.T) {
			bad, badRepo, pin, launch := r, repo, owner.Pin, owner.LaunchID
			probe := &candidateStoppedProbe{}
			switch change {
			case "resource":
				bad.OwnedBranchID = strings.Repeat("6", 32)
			case "branch":
				bad.Branch = "other-task"
			case "commit":
				bad.Commit = strings.Repeat("7", 40)
			case "repository":
				badRepo.Inode++
			case "owner":
				pin.Generation++
			case "launch":
				launch = strings.Repeat("8", 32)
			case "fence":
				probe.deny = true
			}
			if err := c.validateOwnedBranchBinding(ctx, parent, uint32(os.Geteuid()), bad, badRepo, pin, launch, probe); !errors.Is(err, ErrHold) {
				t.Fatal("foreign ownership accepted", err)
			}
		})
	}
	before := integrationApprovalID(integrationqueue.RuntimeItem{Request: r, ExecutionID: owner.Pin.ExecutionID})
	r.OwnedBranchID = strings.Repeat("9", 32)
	if before == integrationApprovalID(integrationqueue.RuntimeItem{Request: r, ExecutionID: owner.Pin.ExecutionID}) {
		t.Fatal("ownership omitted from approval digest")
	}
	if err := c.reserveOwnedBranchApproval(ctx, id, before); err != nil {
		t.Fatal(err)
	}
	if err := c.reserveOwnedBranchApproval(ctx, id, before); err != nil {
		t.Fatal("same approval replay denied", err)
	}
	if err := c.reserveOwnedBranchApproval(ctx, id, strings.Repeat("a", 64)); !errors.Is(err, ErrHold) {
		t.Fatal("second task borrowed branch", err)
	}
}
