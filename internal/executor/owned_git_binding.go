package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"

	"mcpshka/internal/integrationqueue"
)

func (c *Controller) ownedBranchApprovalPins(ctx context.Context, r integrationqueue.Request, repo RepositoryIdentity, pin Pin, launch string) (OwnedGitBranch, error) {
	if c.db == nil || !idPattern.MatchString(r.OwnedBranchID) || !integrationqueue.ValidSourceBranch(r.Branch) || !commitPattern.MatchString(r.Commit) || !validPin(pin) || !idPattern.MatchString(launch) {
		return OwnedGitBranch{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, ownedGitBranchKey(r.OwnedBranchID))
	var born OwnedGitBranch
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || len(raw) > 8192 || d.Decode(&born) != nil || d.Decode(new(any)) != io.EOF || born.Version != 1 || born.State != "READY" || born.ID != r.OwnedBranchID || born.Repository != repo || born.Ref != "refs/heads/"+r.Branch || born.Commit != r.Commit || born.Owner.Pin != pin || born.Owner.LaunchID != launch || born.Owner.PermitCommit != r.Commit || born.Device != repo.Device || born.Inode == 0 {
		return OwnedGitBranch{}, ErrHold
	}
	// Check the sealed reservation as well; another resource may not borrow
	// this name even if it carries the same commit and execution trace.
	want := born
	want.State = "CREATE_INTENT"
	want.Device = 0
	want.Inode = 0
	reservation, _ := json.Marshal(want)
	prior, err := c.db.GetContext(ctx, ownedGitBranchNameKey(repo.CommonDir, born.Ref))
	if err != nil || !bytes.Equal(prior, reservation) {
		return OwnedGitBranch{}, ErrHold
	}
	return born, nil
}

func ownedBranchApprovalKey(id string) string {
	return "executor/owned-git-branch-approvals/" + id + ".json"
}

func (c *Controller) reserveOwnedBranchApproval(ctx context.Context, id, approval string) error {
	if c.db == nil || !idPattern.MatchString(id) || !digestPattern.MatchString(approval) {
		return ErrHold
	}
	raw, _ := json.Marshal(struct {
		Version                int
		ResourceID, ApprovalID string
	}{1, id, approval})
	fresh, err := c.db.PutIfAbsent(ownedBranchApprovalKey(id), raw)
	if err != nil {
		return err
	}
	if !fresh {
		return c.inspectOwnedBranchApproval(ctx, id, approval)
	}
	return nil
}

func (c *Controller) inspectOwnedBranchApproval(ctx context.Context, id, approval string) error {
	if c.db == nil || !idPattern.MatchString(id) || !digestPattern.MatchString(approval) {
		return ErrHold
	}
	want, _ := json.Marshal(struct {
		Version                int
		ResourceID, ApprovalID string
	}{1, id, approval})
	raw, err := c.db.GetContext(ctx, ownedBranchApprovalKey(id))
	if err != nil || !bytes.Equal(raw, want) {
		return ErrHold
	}
	return nil
}

// Called only by root approval issuance. UID/parent/observer parameters are
// private so filesystem contract tests need no production authority.
func (c *Controller) validateOwnedBranchBinding(ctx context.Context, parent string, uid uint32, r integrationqueue.Request, repo RepositoryIdentity, pin Pin, launch string, observer stoppedObserver) error {
	born, err := c.ownedBranchApprovalPins(ctx, r, repo, pin, launch)
	if err != nil {
		return ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, repo, uid); err != nil {
		return ErrHold
	}
	if err := c.candidateOwnerStopped(ctx, born.Owner, observer); err != nil {
		return ErrHold
	}
	root, err := os.OpenRoot(repo.CommonDir)
	if err != nil {
		return ErrHold
	}
	defer root.Close()
	return inspectOwnedGitBranch(root, born, born.Ref, uid)
}
