package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mcpshka/internal/approval"
	"mcpshka/internal/idempotency"
	"os"
	"strings"
)

// ProbeOptions binds protocol-specific ownership and recovery to receipt inspection.
// Callbacks inspect existing evidence; the probe never replays a mutation.
type ProbeOptions struct {
	Contract                 *Contract
	ReceiptKey               string
	HandlerKey               string
	HandlerOperation         string
	PrincipalScoped          bool
	RetirementRejectedPrefix string
	DeploymentOutput         func() (map[string]any, bool)
	HandlerUnknown           func(error) bool
	YieldProof               func(context.Context, string) (map[string]any, bool)
	Observe                  func(context.Context, map[string]any) (Evidence, error)
}
type MutationProbeInput struct {
	Tool  string         `json:"tool"`
	Key   string         `json:"key"`
	Input map[string]any `json:"input,omitempty"`
}

type Receipt[T any] struct {
	IsError   bool     `json:"is_error,omitempty"`
	ErrorText string   `json:"error_text,omitempty"`
	Output    T        `json:"output"`
	Before    Evidence `json:"before"`
	After     Evidence `json:"after"`
}

func ProbeMutation(ctx context.Context, store *idempotency.Store, in MutationProbeInput, opts ProbeOptions) (map[string]any, error) {
	if _, ok := Contracts[in.Tool]; !ok && (opts.Contract == nil || opts.Contract.Validate() != nil) {
		return nil, errors.New("unknown mutation tool")
	}
	if strings.TrimSpace(in.Key) == "" || len(in.Key) > 256 || strings.ContainsRune(in.Key, 0) {
		return nil, errors.New("valid retry key required")
	}
	sum := sha256.Sum256([]byte(opts.ReceiptKey))
	key := "contract:" + hex.EncodeToString(sum[:])
	status, err := store.Lookup(key)
	if errors.Is(err, os.ErrNotExist) {
		// A principal-scoped receipt must not be distinguishable from a
		// foreign principal's receipt through this read-only probe.
		// Keep explicit not_found only for ordinary unscoped operations.
		if opts.PrincipalScoped {
			return nil, errors.New("principal-scoped mutation receipt unavailable")
		}
		return map[string]any{"outcome": "not_found", "retry_allowed": false, "message": "No durable receipt found; reconcile the resource before any mutation."}, nil
	}
	if err != nil {
		return nil, err
	}
	if status.Operation != "contract/"+in.Tool {
		return nil, errors.New("receipt tool mismatch")
	}
	out := map[string]any{"receipt": status, "outcome": "UNKNOWN", "retry_allowed": false}
	if in.Tool == "admin_session_retire_deployment" && status.State == idempotency.Failed && strings.HasPrefix(status.Error, opts.RetirementRejectedPrefix) {
		out["outcome"] = "rejected_before_execution"
	}
	if status.State == idempotency.Completed {
		out["outcome"] = "handler_completed"
	}
	var receipt Receipt[map[string]any]
	_, err = store.ReadResult(key, status.Operation, status.Fingerprint, &receipt)
	if err != nil {
		return nil, err
	}
	if status.State == idempotency.Completed && receipt.IsError {
		out["outcome"] = "handler_reported_error"
		out["handler_is_error"] = true
	}
	// If the server died after the existing handler saved its result but
	// before the outer receipt, recover only safe resource handles. Never
	// rerun the handler or expose its result/lease/approval secrets.
	if status.State != idempotency.Completed {
		if in.Tool == "project_deploy" && opts.DeploymentOutput != nil {
			if output, ok := opts.DeploymentOutput(); ok {
				receipt.Output = output
			}
		}
		handlerKey := opts.HandlerKey
		if handlerKey == "" {
			handlerKey = in.Key
		}
		handlerOperation := opts.HandlerOperation
		if handlerOperation == "" {
			handlerOperation = in.Tool
		}
		if inner, e := store.Lookup(handlerKey); e == nil && inner.Operation == handlerOperation && inner.State == idempotency.Completed {
			if _, e = store.ReadResult(handlerKey, inner.Operation, inner.Fingerprint, &receipt.Output); e == nil {
				out["handler_receipt"] = "completed"
			}
		} else if e == nil && inner.Operation == handlerOperation && inner.State == idempotency.Failed && inner.Fingerprint != "" {
			// runIdempotent keeps unknown-outcome errors PENDING, so a
			// FAILED handler receipt is a definite refusal: a missing
			// parent directory left fs_write UNKNOWN forever (BUG-20261023-70).
			if !opts.HandlerUnknown(errors.New(inner.Error)) {
				out["handler_receipt"] = "failed"
				out["handler_error"] = strings.ToValidUTF8(inner.Error[:min(len(inner.Error), 512)], "")
				out["outcome"] = "handler_failed"
				out["retry_allowed"] = true
				out["message"] = "The handler refused without effect: fix the cause and repeat with a NEW idempotency_key (the old key replays the failure)."
			}
		}
	}
	if status.State != idempotency.Completed && opts.YieldProof != nil {
		if proof, ok := opts.YieldProof(ctx, status.Fingerprint); ok {
			receipt.Output = proof
			out["handler_receipt"] = "atomic_yield_proof"
			out["outcome"] = "release_committed"
		}
	}
	handles := map[string]any{}
	for _, field := range []string{"id", "deployment_id", "run_id", "backup_id", "queue_id", "session_id", "yield_id"} {
		if value, ok := receipt.Output[field].(string); ok && len(value) <= 128 {
			handles[field] = value
		}
	}
	if job, ok := receipt.Output["job"].(map[string]any); ok {
		if value, ok := job["id"].(string); ok && len(value) <= 128 {
			handles["job_id"] = value
		}
	}
	if len(handles) > 0 {
		out["handles"] = handles
	}
	out["before"] = receipt.Before
	out["after"] = receipt.After
	if len(in.Input) > 0 {
		evidence, e := opts.Observe(ctx, receipt.Output)
		out["live"] = evidence
		if e != nil {
			out["live_state"] = "UNKNOWN"
		}
	}
	return out, nil
}

func RequestMutationApproval(store *approval.Store, tool string, normalized map[string]any, terminal bool, owner string) (approval.Request, error) {
	c := Contracts[tool]
	payload := make(map[string]any, len(normalized))
	for k, v := range normalized {
		if k != "contract_approval_token" {
			payload[k] = v
		}
	}
	if terminal {
		payload["_terminal_owner"] = owner
	}
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > 32768 {
		return approval.Request{}, errors.New("approval payload budget exceeded")
	}
	if _, err = RetryKey(c, payload); err != nil {
		return approval.Request{}, err
	}
	fp, err := idempotency.Fingerprint(tool, payload)
	if err != nil {
		return approval.Request{}, err
	}
	out, err := store.RequestWithPlan("mcp:"+tool, []string{fp}, "/root/repos/mcpshka", fp, approval.DefaultTTL)
	return out, err
}
