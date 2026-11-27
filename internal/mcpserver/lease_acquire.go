package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/statestore"
)

// This authority-bearing receipt is retained: expiry must not recreate a released lease.
const leaseAcquireOperation = "project_lease_acquire_atomic/v1"

func acquireProjectLease(ctx context.Context, d *serverDeps, in ProjectLeaseAcquireInput) (projectlease.AcquireResult, error) {
	key := mutationReceiptKey(ctx, "project_lease_acquire", in.IdempotencyKey, nil)
	payload := struct{ Path, Owner string }{in.Path, in.Owner}
	execution := sha256.Sum256([]byte(key))
	admit := func(commit projectlease.AcquireCommit) (out projectlease.AcquireResult, err error) {
		err = d.manager.WithIdleProject(in.Path, func() error {
			if err := d.pipelines.Guard(in.Path, ""); err != nil {
				return err
			}
			if commit == nil {
				out, err = d.leases.AcquireTracked(in.Path, in.Owner, hex.EncodeToString(execution[:16]))
			} else {
				out, err = d.leases.AcquireJournaled(in.Path, in.Owner, hex.EncodeToString(execution[:16]), commit)
			}
			return err
		})
		return out, err
	}
	if d.manager.Database() == nil {
		return runIdempotent(d.idempotencyStore, key, "project_lease_acquire", payload, func() (projectlease.AcquireResult, error) { return admit(nil) })
	}
	store := d.idempotencyStore
	fingerprint, err := idempotency.Fingerprint(leaseAcquireOperation, payload)
	if err != nil {
		return projectlease.AcquireResult{}, err
	}
	begin, err := store.Begin(key, leaseAcquireOperation, fingerprint)
	if err != nil {
		return projectlease.AcquireResult{}, err
	}
	var out projectlease.AcquireResult
	replay := func() (bool, error) {
		state, err := store.ReadResult(key, leaseAcquireOperation, fingerprint, &out)
		if err != nil {
			return false, err
		}
		switch state {
		case idempotency.Completed:
			current, err := d.leases.ValidateHolder(in.Path, out.Token)
			if err != nil {
				out = projectlease.AcquireResult{}
				return false, err
			}
			out.Status = current
			return true, nil
		case idempotency.Pending:
			return false, nil
		}
		return false, errors.New("lease admission receipt is terminal; reconcile original request")
	}
	if !begin.Fresh {
		if done, err := replay(); done || err != nil {
			return out, err
		}
	}
	out, err = admit(func(authority *statestore.DB, recordKey string, data []byte, gate string, result projectlease.AcquireResult) error {
		return store.CompleteWithRecord(authority, key, leaseAcquireOperation, fingerprint, result, recordKey, data, gate)
	})
	if err == nil {
		return out, nil
	}
	cause := d.leases.BusyCause(in.Path, in.Owner, err)
	if done, err := replay(); done || err != nil {
		return out, err
	}
	// Deleting this exact PENDING intent fences any delayed paired commit.
	// A lease cannot exist without its completed credential receipt.
	if err := store.Discard(key, leaseAcquireOperation, fingerprint); err == nil {
		return projectlease.AcquireResult{}, failure.Rejected("LEASE_ADMISSION_NO_EFFECT", "project_lease_acquire", "lease admission left no effect; inspect the current owner or policy", cause)
	}
	if done, err := replay(); done || err != nil {
		return out, err
	}
	return projectlease.AcquireResult{}, cause
}
