package mcpserver

import (
	"errors"
	"fmt"
	"mcpshka/internal/audit"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/rootbroker"
	"os"
)

const retirementRejectedBeforeWorker = "fixed-retirement-v1-rejected-before-worker: "

func (r *toolRegistry) retirementPreflight(name string, input map[string]any, key, operation, fingerprint string) error {
	if name != "admin_session_retire_deployment" {
		return nil
	}
	reason, _ := input["reason"].(string)
	invalid := rootbroker.ValidateRetirementReasonV1(reason)
	if invalid == nil {
		return nil
	}
	prior, err := r.store.Lookup(key)
	if errors.Is(err, os.ErrNotExist) {
		return invalid
	}
	if err != nil {
		return err
	}
	if prior.Operation != operation || prior.Fingerprint != fingerprint {
		return idempotency.ErrConflict
	}
	switch prior.State {
	case idempotency.Pending:
		// The fixed v1 broker rejects this exact field before it can launch a worker.
		if r.audit != nil {
			if err := r.audit.Log(audit.Event{Type: "mutation_contract", Result: "before_rejection_reconciliation", Details: map[string]any{"tool": name, "receipt_key": key, "fingerprint": fingerprint}}); err != nil {
				return errors.New("cannot persist retirement rejection audit; HOLD")
			}
		}
		if err := r.store.FailPendingCAS(key, operation, fingerprint, errors.New(retirementRejectedBeforeWorker+invalid.Error())); err != nil {
			return fmt.Errorf("retirement rejection reconciliation held: %w", err)
		}
	case idempotency.Failed:
		if prior.Error != retirementRejectedBeforeWorker+invalid.Error() {
			return errors.New("retirement failed receipt lacks pre-worker proof; HOLD")
		}
	default:
		return errors.New("invalid retirement has a completed receipt; HOLD")
	}
	return invalid
}
