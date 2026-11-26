package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/integrationqueue"
)

// IntegrationClaim is a root-store reservation. It is not itself proof that
// native/legacy writers have been fenced. The coordinator must hold the shared
// admission barrier and verify those writers before beginning an effect.
type IntegrationClaim struct {
	Version     int                `json:"version"`
	Repository  RepositoryIdentity `json:"repository"`
	ApprovalID  string             `json:"approval_id"`
	Owner       string             `json:"owner"`
	ExecutionID string             `json:"execution_id"`
	Candidate   string             `json:"candidate"`
	Generation  uint64             `json:"generation"`
	State       string             `json:"state"`
	CreatedAt   time.Time          `json:"created_at"`
}

// This observation is private to the trusted supervisor. There is deliberately
// no broker/API completion endpoint accepting a caller's success flag or SHA.
// The future effect supervisor must obtain both values from fresh direct Git
// probes after the exact integration worker has been verifiably fenced.
type integrationObservation struct {
	Repository RepositoryIdentity
	Head       string
	Receipt    string
}

func integrationClaimKey(common string) string {
	sum := sha256.Sum256([]byte(common))
	return "executor/integration-claims/" + hex.EncodeToString(sum[:]) + ".json"
}

func integrationCompletionKey(execution string) string {
	return "executor/integration-completions/" + execution + ".json"
}

func validIntegrationClaim(c IntegrationClaim, common string) bool {
	return c.Version == 1 && c.Repository.CommonDir == common && filepath.IsAbs(common) && filepath.Clean(common) == common && strings.HasPrefix(common, "/root/repos/") &&
		c.Repository.Device != 0 && c.Repository.Inode != 0 && digestPattern.MatchString(c.ApprovalID) && idPattern.MatchString(c.ExecutionID) && commitPattern.MatchString(c.Candidate) && c.Owner != "" && len(c.Owner) <= 256 && !strings.ContainsAny(c.Owner, "\x00\r\n") && c.Generation > 0 &&
		(c.State == "HELD" || c.State == "EFFECT_INTENT" || c.State == "RELEASED" || c.State == "FAILED") && !c.CreatedAt.IsZero() && !c.CreatedAt.After(time.Now().Add(time.Minute))
}

func (c *Controller) readIntegrationClaim(ctx context.Context, common string) (IntegrationClaim, []byte, error) {
	if c.db == nil {
		return IntegrationClaim{}, nil, ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationClaimKey(common))
	if err != nil {
		return IntegrationClaim{}, nil, err
	}
	claim, err := decodeIntegrationClaim(raw, common)
	if err == nil && (claim.State == "RELEASED" || claim.State == "FAILED") {
		completed, readErr := c.db.GetContext(ctx, integrationCompletionKey(claim.ExecutionID))
		if readErr != nil || !bytes.Equal(completed, raw) {
			return IntegrationClaim{}, nil, ErrHold
		}
	}
	return claim, raw, err
}

func decodeIntegrationClaim(raw []byte, common string) (IntegrationClaim, error) {
	var claim IntegrationClaim
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) > 8192 || dec.Decode(&claim) != nil || dec.Decode(new(any)) != io.EOF || !validIntegrationClaim(claim, common) {
		return IntegrationClaim{}, ErrHold
	}
	return claim, nil
}

func sameIntegrationClaim(a, b IntegrationClaim) bool {
	return a.Version == b.Version && a.Repository == b.Repository && a.ApprovalID == b.ApprovalID && a.Owner == b.Owner && a.ExecutionID == b.ExecutionID && a.Candidate == b.Candidate && a.Generation == b.Generation && a.CreatedAt == b.CreatedAt
}

func (c *Controller) reserveIntegrationClaim(ctx context.Context, a IntegrationApproval) (IntegrationClaim, error) {
	item := integrationqueue.RuntimeItem{Request: a.Request, ExecutionID: a.ExecutionID, State: integrationqueue.Pending}
	stored, err := c.inspectIntegrationApproval(ctx, item, a.Repository)
	if err != nil || stored != a {
		return IntegrationClaim{}, ErrHold
	}
	// An execution ID cannot be rebound to another permit/repository, even if
	// its first claim attempt lost a reply or found a different writer busy.
	binding, _ := json.Marshal(struct {
		ApprovalID string
		Repository RepositoryIdentity
	}{a.ID, a.Repository})
	bindingKey := "executor/integration-executions/" + a.ExecutionID + ".json"
	ok, err := c.db.PutIfAbsent(bindingKey, binding)
	if err != nil {
		return IntegrationClaim{}, err
	}
	if !ok {
		existing, err := c.db.GetContext(ctx, bindingKey)
		if err != nil || !bytes.Equal(existing, binding) {
			return IntegrationClaim{}, ErrHold
		}
	}
	for range 32 {
		if err := ctx.Err(); err != nil {
			return IntegrationClaim{}, err
		}
		previous, raw, err := c.readIntegrationClaim(ctx, a.Repository.CommonDir)
		completedRaw, completionErr := c.db.GetContext(ctx, integrationCompletionKey(a.ExecutionID))
		if completionErr == nil {
			completed, decodeErr := decodeIntegrationClaim(completedRaw, a.Repository.CommonDir)
			if decodeErr != nil || (completed.State != "RELEASED" && completed.State != "FAILED") || completed.ApprovalID != a.ID || completed.Repository != a.Repository || completed.Owner != a.Request.Owner || completed.ExecutionID != a.ExecutionID || completed.Candidate != a.Request.Commit || err != nil || previous.Repository != a.Repository || previous.Generation < completed.Generation || (previous.Generation == completed.Generation && !sameIntegrationClaim(previous, completed)) {
				return IntegrationClaim{}, ErrHold
			}
			if sameIntegrationClaim(previous, completed) {
				// Crash after writing completion but before retiring the shared
				// reservation: retain intent for supervisor reconciliation.
				if previous.State == "HELD" {
					return IntegrationClaim{}, ErrHold
				}
				return previous, nil
			}
			return completed, nil
		} else if !errors.Is(completionErr, os.ErrNotExist) {
			return IntegrationClaim{}, completionErr
		}
		generation := uint64(1)
		if err == nil {
			if previous.Repository != a.Repository {
				return IntegrationClaim{}, ErrHold
			}
			if previous.ApprovalID == a.ID && previous.Owner == a.Request.Owner && previous.ExecutionID == a.ExecutionID && previous.Candidate == a.Request.Commit {
				return previous, nil
			}
			if (previous.State != "RELEASED" && previous.State != "FAILED") || previous.Generation == ^uint64(0) {
				return IntegrationClaim{}, ErrHold
			}
			generation = previous.Generation + 1
		} else if !errors.Is(err, os.ErrNotExist) {
			return IntegrationClaim{}, err
		}
		claim := IntegrationClaim{Version: 1, Repository: a.Repository, ApprovalID: a.ID, Owner: a.Request.Owner, ExecutionID: a.ExecutionID, Candidate: a.Request.Commit, Generation: generation, State: "HELD", CreatedAt: time.Now().UTC()}
		next, _ := json.Marshal(claim)
		if raw == nil {
			ok, err = c.db.PutIfAbsent(integrationClaimKey(a.Repository.CommonDir), next)
		} else {
			ok, err = c.db.Replace(integrationClaimKey(a.Repository.CommonDir), raw, next)
		}
		if err != nil {
			return IntegrationClaim{}, err
		}
		if ok {
			return claim, nil
		}
	}
	return IntegrationClaim{}, ErrHold
}

// Persist before any external mutation. An existing EFFECT_INTENT is UNKNOWN,
// not authorization to start the effect again after reconnect or reboot.
func (c *Controller) beginIntegrationEffect(ctx context.Context, pinned IntegrationClaim) error {
	current, raw, err := c.readIntegrationClaim(ctx, pinned.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(current, pinned) || current.State != "HELD" {
		return ErrHold
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current.State = "EFFECT_INTENT"
	next, _ := json.Marshal(current)
	ok, err := c.db.Replace(integrationClaimKey(current.Repository.CommonDir), raw, next)
	if err != nil {
		return err
	}
	if !ok {
		return ErrHold
	}
	return nil
}

// Only the trusted, fenced effect supervisor may call this storage transition.
// No external success flag, exit code, age or missing stdout releases a claim.
func (c *Controller) finishIntegrationClaim(ctx context.Context, pinned IntegrationClaim, observed integrationObservation) error {
	current, raw, err := c.readIntegrationClaim(ctx, pinned.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(current, pinned) || observed.Repository != current.Repository || observed.Head != current.Candidate || observed.Receipt != current.Candidate {
		return ErrHold
	}
	if current.State == "RELEASED" {
		return nil
	}
	if current.State != "EFFECT_INTENT" {
		return ErrHold
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current.State = "RELEASED"
	next, _ := json.Marshal(current)
	// Completion precedes releasing the reusable repository record. Otherwise
	// a later generation would erase the only evidence preventing replay of
	// this execution ID. A lost retirement reply leaves a recoverable intent.
	stored, err := c.db.PutIfAbsent(integrationCompletionKey(current.ExecutionID), next)
	if err != nil {
		return err
	}
	if !stored {
		prior, err := c.db.GetContext(ctx, integrationCompletionKey(current.ExecutionID))
		if err != nil || !bytes.Equal(prior, next) {
			return ErrHold
		}
	}
	ok, err := c.db.Replace(integrationClaimKey(current.Repository.CommonDir), raw, next)
	if err != nil {
		return err
	}
	if !ok {
		return ErrHold
	}
	return nil
}
