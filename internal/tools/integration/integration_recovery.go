package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/executor"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tooldef"
)

type IntegrationRecoveryInput struct {
	PermitID       string `json:"permit_id"`
	ApprovalID     string `json:"approval_id"`
	Workdir        string `json:"workdir"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func (svc Service) RecoveryCall(ctx context.Context, m *jobs.Manager, in IntegrationRecoveryInput, status bool) (executor.IntegrationClaim, error) {
	if !executorCanaryID.MatchString(in.PermitID) || !integrationApprovalDigest.MatchString(in.ApprovalID) || (!status && strings.TrimSpace(in.IdempotencyKey) == "") {
		return executor.IntegrationClaim{}, errors.New("integration recovery requires fixed permit, approval and retry identities")
	}
	a, err := ApprovalCall(ctx, m, IntegrationApprovalInput{PermitID: in.PermitID, ApprovalID: in.ApprovalID, Workdir: in.Workdir})
	if err != nil {
		return executor.IntegrationClaim{}, err
	}
	return svc.RecoveryWithApproval(ctx, m, in, status, a, func(ctx context.Context, action, permit, approval string) ([]byte, error) {
		return brokerclient.Run(ctx, "executor", action, permit, approval)
	})
}

// Only the cold root-approval boundary above supplies this evidence in
// production. The private split lets isolated tests prove that foreign owners
// never reach a mutating broker action, without changing the live broker.
func (svc Service) RecoveryWithApproval(ctx context.Context, m *jobs.Manager, in IntegrationRecoveryInput, status bool, a executor.IntegrationApproval, invoke func(context.Context, string, string, string) ([]byte, error)) (executor.IntegrationClaim, error) {
	if a.ID != in.ApprovalID || a.TestPin.PermitID != in.PermitID || invoke == nil {
		return executor.IntegrationClaim{}, errors.New("integration recovery approval pins differ: HOLD")
	}
	if !status && a.Request.Owner != svc.Owner(ctx) {
		return executor.IntegrationClaim{}, errors.New("integration recovery belongs to another authenticated owner: HOLD")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	action := "integration-recovery-status"
	if !status {
		action = "integration-recover"
	}
	var raw []byte
	var err error
	call := func() error {
		var err error
		raw, err = invoke(ctx, action, in.PermitID, in.ApprovalID)
		return err
	}
	if status {
		err = call()
	} else {
		err = m.WithIdleProject(a.Request.Path, call)
	}
	if err != nil {
		return executor.IntegrationClaim{}, err
	}
	var claim executor.IntegrationClaim
	if err := DecodeInput(string(raw), &claim); err != nil {
		return executor.IntegrationClaim{}, err
	}
	if claim.Version != 1 || claim.ApprovalID != a.ID || claim.Repository != a.Repository || claim.ExecutionID != a.ExecutionID || claim.Candidate != a.Request.Commit || claim.Owner != a.Request.Owner || claim.Generation == 0 || claim.CreatedAt.IsZero() || !ValidRecoveryState(claim.State, status) || (!status && claim.State != "RELEASED") {
		return executor.IntegrationClaim{}, errors.New("integration recovery evidence pins differ: HOLD")
	}
	return claim, nil
}

func (svc Service) RegisterRecovery(add func(tooldef.Def), m *jobs.Manager) {
	add(tooldef.New(&mcp.Tool{Name: "integration_recovery_status", Description: "Read the root-issued integration reservation and historical completion for a fixed candidate approval. EFFECT_INTENT remains UNKNOWN; age does not release it.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationRecoveryInput) (*mcp.CallToolResult, executor.IntegrationClaim, error) {
		out, err := svc.RecoveryCall(ctx, m, in, true)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "integration_recover", Description: "Reconcile this authenticated owner's integration using sealed worker binding, closed admission, native writer fence, fresh stopped-incarnation and Git evidence. Never repeats Git or stops unknown workers. Holds while the production native writer barrier is unavailable.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationRecoveryInput) (*mcp.CallToolResult, executor.IntegrationClaim, error) {
		out, err := svc.RecoveryCall(ctx, m, in, false)
		return nil, out, err
	}))
}

func RecoveryCompatInput(in ExecStartInput) (IntegrationRecoveryInput, error) {
	if len(in.Args) != 2 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return IntegrationRecoveryInput{}, errors.New("integration-recover requires one bounded JSON request and no overrides")
	}
	var v IntegrationRecoveryInput
	if err := DecodeInput(in.Args[1], &v); err != nil {
		return IntegrationRecoveryInput{}, err
	}
	if v.Workdir != in.Workdir || (v.IdempotencyKey != "" && v.IdempotencyKey != in.IdempotencyKey) {
		return IntegrationRecoveryInput{}, errors.New("integration-recover scope/retry pins differ")
	}
	v.IdempotencyKey = in.IdempotencyKey
	if !executorCanaryID.MatchString(v.PermitID) || !integrationApprovalDigest.MatchString(v.ApprovalID) || strings.TrimSpace(v.IdempotencyKey) == "" {
		return IntegrationRecoveryInput{}, errors.New("integration recovery requires fixed permit, approval and retry identities")
	}
	return v, nil
}

func (svc Service) RecoveryCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	if err := svc.Authorize(ctx, &mcp.Tool{Name: "integration_recover", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}); err != nil {
		return jobs.Snapshot{}, err
	}
	v, err := RecoveryCompatInput(in)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	out, err := svc.RecoveryCall(ctx, m, v, false)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	owner := sha256.Sum256([]byte(svc.Owner(ctx)))
	request := sha256.Sum256([]byte(in.Args[1]))
	return m.StoreReceipt(in.Command, []string{"integration-recover", "request-sha256:" + hex.EncodeToString(request[:]), "owner-sha256:" + hex.EncodeToString(owner[:])}, in.Workdir, string(raw))
}

func ValidRecoveryState(state string, status bool) bool {
	if !status {
		return state == "RELEASED"
	}
	return state == "HELD" || state == "EFFECT_INTENT" || state == "RELEASED" || state == "FAILED"
}
