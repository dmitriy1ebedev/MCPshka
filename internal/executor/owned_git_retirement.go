package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"
)

type GitBranchRetirement struct {
	Version     int    `json:"version"`
	ApprovalID  string `json:"approval_id"`
	ResourceID  string `json:"resource_id"`
	ExecutionID string `json:"execution_id"`
	Generation  uint64 `json:"generation"`
	BirthSHA256 string `json:"birth_sha256"`
	RecoveryID  string `json:"recovery_id"`
	State       string `json:"state"`
}

func gitBranchRetirementKey(id string) string {
	return "executor/git-branch-retirements/" + id + ".json"
}

// RetireOwnedGitBranch is a root supervisor continuation of an already sealed
// successful integration. An approval without owned source proof stays HOLD.
func RetireOwnedGitBranch(ctx context.Context, id string) (GitBranchRetirement, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return GitBranchRetirement{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return GitBranchRetirement{}, err
	}
	if a.Request.OwnedBranchID == "" {
		return GitBranchRetirement{Version: 1, ApprovalID: id, State: "HOLD"}, ErrHold
	}
	runtime, err := c.integrationRuntime(ctx, a)
	if err != nil || runtime.Claim.State != "RELEASED" || !runtime.Dispatched || runtime.Worker.State != "FENCED" || len(runtime.Worker.Launches) != 1 || runtime.Worker.Launches[0].Identity == nil || runtime.Worker.Pin.PermitID == a.TestPin.PermitID {
		return GitBranchRetirement{}, ErrHold
	}
	var result GitBranchRetirement
	// Integration dispatch takes worker permit before repository; source test
	// permits are distinct and are acquired last, never in the opposite order.
	err = c.WithPermitLock(ctx, runtime.Worker.Pin, func() error {
		return c.withIntegrationLock(ctx, a.Repository, func() error {
			return (liveIntegrationRecovery{controller: c}).WithWriterFence(ctx, a, func() error {
				return c.WithPermitLock(ctx, a.TestPin, func() error {
					proof := func() error {
						current, _, err := c.readIntegrationClaim(ctx, a.Repository.CommonDir)
						if err != nil || (current.State != "RELEASED" && current.State != "FAILED") || current.Generation < runtime.Claim.Generation {
							return ErrHold
						}
						fresh, err := c.integrationRuntime(ctx, a)
						if err != nil || !fresh.Dispatched || fresh.Claim != runtime.Claim || fresh.Launch != runtime.Launch || fresh.Worker.State != "FENCED" || len(fresh.Worker.Launches) != 1 || fresh.Worker.Launches[0].Identity == nil {
							return ErrHold
						}
						l := fresh.Worker.Launches[0]
						if l.State != "FENCED" || (SystemdFence{}).VerifyStopped(ctx, l.ID, *l.Identity) != nil {
							return ErrHold
						}
						return c.candidateOwnerStopped(ctx, CandidateOwner{a.TestPin, a.LaunchID, a.Request.Commit}, SystemdFence{})
					}
					var err error
					result, err = c.retireOwnedGitBranchFiles(ctx, ProtectedRepositoryRoot, 0, a, runtime.Claim.Generation, SystemdFence{}, proof, nil)
					return err
				})
			})
		})
	})
	return result, err
}

// The callback is compiled supervisor proof, never a caller Boolean. Root
// locks remain held across fresh proof, filesystem checks and unlink.
func (c *Controller) retireOwnedGitBranchFiles(ctx context.Context, parent string, uid uint32, a IntegrationApproval, generation uint64, observer stoppedObserver, proof func() error, afterDelete func() error) (GitBranchRetirement, error) {
	if ctx.Err() != nil {
		return GitBranchRetirement{}, ctx.Err()
	}
	if generation == 0 || proof == nil || !digestPattern.MatchString(a.ID) || !idPattern.MatchString(a.ExecutionID) {
		return GitBranchRetirement{}, ErrHold
	}
	if err := c.inspectOwnedBranchApproval(ctx, a.Request.OwnedBranchID, a.ID); err != nil {
		return GitBranchRetirement{}, ErrHold
	}
	born, err := c.ownedBranchApprovalPins(ctx, a.Request, a.Repository, a.TestPin, a.LaunchID)
	if err != nil {
		return GitBranchRetirement{}, ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, born.Repository, uid); err != nil {
		return GitBranchRetirement{}, ErrHold
	}
	root, err := os.OpenRoot(born.Repository.CommonDir)
	if err != nil {
		return GitBranchRetirement{}, ErrHold
	}
	defer root.Close()
	encoded, _ := json.Marshal(born)
	digest := sha256.Sum256(encoded)
	recoverySum := sha256.Sum256([]byte("owned-git-recovery:" + born.ID))
	want := GitBranchRetirement{Version: 1, ApprovalID: a.ID, ResourceID: born.ID, ExecutionID: a.ExecutionID, Generation: generation, BirthSHA256: hex.EncodeToString(digest[:]), RecoveryID: hex.EncodeToString(recoverySum[:16]), State: "DELETE_INTENT"}
	receipt := want
	raw, readErr := c.db.GetContext(ctx, gitBranchRetirementKey(a.ID))
	if readErr == nil {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if len(raw) > 8192 || d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF {
			return receipt, ErrHold
		}
		pins := receipt
		pins.State = want.State
		if pins != want || (receipt.State != "DELETE_INTENT" && receipt.State != "DELETED") {
			return receipt, ErrHold
		}
	} else if !os.IsNotExist(readErr) {
		return receipt, readErr
	}
	if readErr == nil {
		if _, err := root.Lstat(born.Ref); os.IsNotExist(err) {
			if err := c.verifyOwnedBranchRecovery(ctx, root, born, receipt.RecoveryID, uid); err != nil {
				return receipt, err
			}
			if receipt.State == "DELETED" {
				return receipt, nil
			}
			if err := syncProtectedRefDirectory(root, born.Ref); err != nil {
				return receipt, ErrHold
			}
			return c.finishOwnedBranchRetirement(receipt, raw)
		} else if err != nil || receipt.State == "DELETED" {
			return receipt, ErrHold
		}
	}
	if err := proof(); err != nil {
		return receipt, ErrHold
	}
	if err := inspectOwnedGitBranch(root, born, born.Ref, uid); err != nil {
		return receipt, err
	}
	// Require current reachability as well as the sealed completion receipt.
	head, err := readProtectedDirectRef(root, "refs/heads/"+a.Request.TargetBranch)
	if err != nil || protectedFastForward(ctx, root, born.Commit, head) != nil {
		return receipt, ErrHold
	}
	recoveryRef := "refs/mcpshka/recovery/owned-branches/" + born.ID
	if _, err := c.prepareOwnedGitRef(ctx, parent, receipt.RecoveryID, born.Repository, recoveryRef, born.Owner, uid, observer, nil); err != nil {
		return receipt, err
	}
	if err := c.verifyOwnedBranchRecovery(ctx, root, born, receipt.RecoveryID, uid); err != nil {
		return receipt, err
	}
	if os.IsNotExist(readErr) {
		raw, _ = json.Marshal(want)
		fresh, err := c.db.PutIfAbsent(gitBranchRetirementKey(a.ID), raw)
		if err != nil || !fresh {
			return receipt, ErrHold
		}
	}
	if err := proof(); err != nil {
		return receipt, ErrHold
	}
	if err := inspectOwnedGitBranch(root, born, born.Ref, uid); err != nil {
		return receipt, err
	}
	if err := root.Remove(born.Ref); err != nil {
		return receipt, ErrHold
	}
	if err := syncProtectedRefDirectory(root, born.Ref); err != nil {
		return receipt, ErrHold
	}
	if afterDelete != nil {
		if err := afterDelete(); err != nil {
			return receipt, err
		}
	}
	return c.finishOwnedBranchRetirement(receipt, raw)
}

func (c *Controller) finishOwnedBranchRetirement(receipt GitBranchRetirement, raw []byte) (GitBranchRetirement, error) {
	receipt.State = "DELETED"
	next, _ := json.Marshal(receipt)
	ok, err := c.db.Replace(gitBranchRetirementKey(receipt.ApprovalID), raw, next)
	if err != nil || !ok {
		return receipt, ErrHold
	}
	return receipt, nil
}

func (c *Controller) verifyOwnedBranchRecovery(ctx context.Context, root *os.Root, source OwnedGitBranch, recoveryID string, uid uint32) error {
	raw, err := c.db.GetContext(ctx, ownedGitBranchKey(recoveryID))
	var recovery OwnedGitBranch
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || len(raw) > 8192 || d.Decode(&recovery) != nil || d.Decode(new(any)) != io.EOF || recovery.Version != 1 || recovery.State != "READY" || recovery.ID != recoveryID || recovery.Repository != source.Repository || recovery.Ref != "refs/mcpshka/recovery/owned-branches/"+source.ID || recovery.Commit != source.Commit || recovery.Owner != source.Owner || recovery.Device != source.Device || recovery.Inode == 0 {
		return ErrHold
	}
	if err := inspectOwnedGitBranch(root, recovery, recovery.Ref, uid); err != nil {
		return err
	}
	return validateProtectedObjects(ctx, root, recovery.Commit)
}
