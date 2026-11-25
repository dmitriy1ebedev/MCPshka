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

type integrationAbortBackend interface {
	WithWriterFence(context.Context, IntegrationApproval, func() error) error
	VerifyStopped(context.Context, string, Identity) error
	ObserveNoEffect(context.Context, IntegrationApproval) error
}

// A failed terminal claim is deliberately distinct from RELEASED success.
// It permanently consumes the execution ID and allows a new execution/generation.
func (c *Controller) failIntegrationClaim(ctx context.Context, pinned IntegrationClaim) error {
	current, raw, err := c.readIntegrationClaim(ctx, pinned.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(current, pinned) {
		return ErrHold
	}
	if current.State == "FAILED" {
		return nil
	}
	if current.State != "HELD" && current.State != "EFFECT_INTENT" {
		return ErrHold
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current.State = "FAILED"
	next, _ := json.Marshal(current)
	made, err := c.db.PutIfAbsent(integrationCompletionKey(current.ExecutionID), next)
	if err != nil {
		return err
	}
	if !made {
		prior, err := c.db.GetContext(ctx, integrationCompletionKey(current.ExecutionID))
		if err != nil || !bytes.Equal(prior, next) {
			return ErrHold
		}
	}
	// Completion precedes retirement; a crash here is recovered by this exact
	// supervised path, never by permitting the same execution to launch again.
	ok, err := c.db.Replace(integrationClaimKey(current.Repository.CommonDir), raw, next)
	if err != nil {
		return err
	}
	if !ok {
		return ErrHold
	}
	return nil
}
func (c *Controller) abortIntegrationClaim(ctx context.Context, a IntegrationApproval, b integrationAbortBackend) error {
	stored, err := c.integrationApprovalForID(ctx, a.ID)
	if err != nil || stored != a || b == nil {
		return ErrHold
	}
	claim, err := c.integrationClaimForApproval(ctx, a)
	if err != nil {
		return ErrHold
	}
	if claim.State == "FAILED" {
		return nil
	}
	if claim.State != "HELD" && claim.State != "EFFECT_INTENT" {
		return ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationWorkerKey(claim.ExecutionID))
	if err != nil || len(raw) > 8192 {
		return ErrHold
	}
	var binding integrationWorkerBinding
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&binding) != nil || d.Decode(new(any)) != io.EOF || binding.Version != 1 || binding.Claim.State != "HELD" || !sameIntegrationClaim(binding.Claim, claim) || !validPin(binding.Pin) || binding.Pin.ExecutionID != claim.ExecutionID || binding.Pin.Generation != claim.Generation || binding.Pin.PermitID == a.TestPin.PermitID || !idPattern.MatchString(binding.LaunchID) || binding.LaunchID == a.LaunchID {
		return ErrHold
	}
	s, err := c.Inspect(ctx, binding.Pin)
	if err != nil || s.State != "FENCED" || len(s.Launches) != 1 || s.Launches[0].ID != binding.LaunchID || s.Launches[0].State != "FENCED" || s.Launches[0].Identity == nil {
		return ErrHold
	}
	return b.WithWriterFence(ctx, a, func() error {
		if err := b.VerifyStopped(ctx, binding.LaunchID, *s.Launches[0].Identity); err != nil {
			return ErrHold
		}
		if err := b.ObserveNoEffect(ctx, a); err != nil {
			return ErrHold
		}
		return c.failIntegrationClaim(ctx, claim)
	})
}
func (b liveIntegrationRecovery) ObserveNoEffect(ctx context.Context, a IntegrationApproval) error {
	if b.controller == nil || !b.controller.authority || os.Geteuid() != 0 {
		return ErrHold
	}
	return b.controller.observeProtectedNoEffect(ctx, ProtectedRepositoryRoot, a, 0)
}
func (c *Controller) observeProtectedNoEffect(ctx context.Context, parent string, a IntegrationApproval, owner uint32) error {
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return ErrHold
	}
	if _, err := c.db.GetContext(ctx, protectedEffectKey(a.ExecutionID)); !os.IsNotExist(err) {
		return ErrHold
	}
	root, err := os.OpenRoot(a.Repository.CommonDir)
	if err != nil {
		return ErrHold
	}
	defer root.Close()
	sum := sha256.Sum256([]byte(a.ExecutionID))
	receipt := "refs/mcpshka/integration/" + hex.EncodeToString(sum[:])
	if _, err := readProtectedDirectRef(root, receipt); !os.IsNotExist(err) {
		return ErrHold
	}
	head, err := readProtectedDirectRef(root, "refs/heads/"+a.Request.TargetBranch)
	if err != nil && !os.IsNotExist(err) {
		return ErrHold
	}
	// The pinned target head may be stale (another item advanced the branch,
	// MCP-101): with no effect intent and no receipt, only the candidate itself
	// on the branch is evidence that this worker published.
	if err == nil && head != a.Request.TargetHead && head == a.Request.Commit {
		return ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return ErrHold
	}
	return nil
}
func AbortProtectedIntegration(ctx context.Context, id string) (IntegrationClaim, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationClaim{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationClaim{}, err
	}
	err = c.withIntegrationLock(ctx, a.Repository, func() error { return c.abortIntegrationClaim(ctx, a, liveIntegrationRecovery{controller: c}) })
	if err != nil {
		return IntegrationClaim{}, err
	}
	return c.integrationClaimForApproval(ctx, a)
}
