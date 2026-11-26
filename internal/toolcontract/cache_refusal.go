package toolcontract

import (
	"encoding/json"
	"errors"

	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
)

type cacheRefusal struct {
	Failure failure.Error `json:"failure"`
}

func (r cacheRefusal) valid() bool {
	return r.Failure.Code == failure.GoCacheMaintenance && r.Failure.NoEffect && !r.Failure.Hold &&
		r.Failure.RetryAfterSeconds > 0
}

// ReplayCacheRefusal accepts only the typed pre-dispatch refusal saved by
// the outer contract. Legacy failed or pending records prove nothing here.
func ReplayCacheRefusal(entry idempotency.Entry) error {
	if entry.State != idempotency.Failed || len(entry.Result) == 0 {
		return nil
	}
	var receipt cacheRefusal
	if json.Unmarshal(entry.Result, &receipt) != nil || !receipt.valid() {
		return nil
	}
	return &cacheRefusalError{text: entry.Error, detail: &receipt.Failure}
}

type cacheRefusalError struct {
	text   string
	detail *failure.Error
}

func (e *cacheRefusalError) Error() string { return e.text }
func (e *cacheRefusalError) Unwrap() error { return e.detail }

func RecordCacheRefusal(store *idempotency.Store, key, operation, fingerprint string, err error) error {
	detail := failure.Details(err)
	if detail == nil || !(cacheRefusal{Failure: *detail}).valid() {
		return errors.New("expected verified retryable Go cache refusal")
	}
	saved := *detail
	saved.Receipt = map[string]string{"state": "failed", "outcome": "rejected_before_execution"}
	if persistErr := store.FailWithResult(key, operation, fingerprint, err, cacheRefusal{Failure: saved}); persistErr != nil {
		return failure.NewHold(failure.FencedUnknown, "RECEIPT_WRITE_FAILED", "mutation_contract", "Go cache refusal could not be recorded; reconcile the retry key", persistErr)
	}
	return &cacheRefusalError{text: err.Error(), detail: &saved}
}
