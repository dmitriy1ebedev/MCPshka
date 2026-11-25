package idempotency

import (
	"errors"
	"fmt"
	"mcpshka/internal/failure"
	"strings"
)

func Run[T any](store *Store, key, operation string, payload any, fn func() (T, error)) (T, error) {
	var zero T
	if strings.TrimSpace(key) == "" {
		return fn()
	}
	fingerprint, err := Fingerprint(operation, payload)
	if err != nil {
		return zero, err
	}
	begin, err := store.Begin(key, operation, fingerprint)
	if err != nil {
		return zero, err
	}
	if !begin.Fresh {
		var replay T
		state, readErr := store.ReadResult(key, operation, fingerprint, &replay)
		if readErr != nil {
			return zero, readErr
		}
		switch state {
		case Completed:
			return replay, nil
		case Failed:
			if begin.Entry.Error == "" {
				return zero, errors.New("previous idempotent attempt failed")
			}
			return zero, errors.New("previous idempotent attempt failed: " + begin.Entry.Error)
		case Pending:
			return zero, errors.New("idempotent operation is PENDING; previous outcome is unknown, reconcile state before retrying with a new key")
		default:
			return zero, fmt.Errorf("invalid idempotency state %q", state)
		}
	}

	out, err := fn()
	if err != nil {
		var unknown interface{ UnknownOutcome() bool }
		if errors.As(err, &unknown) && unknown.UnknownOutcome() {
			return zero, err
		}
		// A refusal proven to precede any effect frees the key for the
		// corrected retry instead of replaying "previous attempt failed"
		// forever (BUG-20261103-5).
		if failure.IsNoEffect(err) && store.Discard(key, operation, fingerprint) == nil {
			return zero, err
		}
		if persistErr := store.Fail(key, operation, fingerprint, err); persistErr != nil {
			return zero, fmt.Errorf("%w; additionally failed to persist idempotency failure: %v", err, persistErr)
		}
		return zero, err
	}
	if err := store.Complete(key, operation, fingerprint, out); err != nil {
		return zero, fmt.Errorf("operation completed but idempotency completion could not be persisted; outcome is unknown: %w", err)
	}
	return out, nil
}

// RejectBeforeAudit releases a fresh receipt when the initial audit failed
// before its handler could start. The original receipt of a replay stays held.
func (store *Store) RejectBeforeAudit(key string, begin BeginResult, cause error) error {
	if begin.Fresh && store.Discard(key, begin.Entry.Operation, begin.Entry.Fingerprint) == nil {
		return failure.Rejected("MUTATION_AUDIT_UNAVAILABLE", "mcpserver.mutate", "before audit could not be written; handler never ran", cause)
	}
	return errors.New("cannot persist before audit; mutation held")
}
