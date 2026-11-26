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

type IntegrationWorkerInput struct {
	PermitID       string `json:"permit_id"`
	ApprovalID     string `json:"approval_id"`
	Workdir        string `json:"workdir"`
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type IntegrationWorkerOutput struct {
	Runtime *executor.IntegrationRuntime `json:"runtime,omitempty"`
	Claim   *executor.IntegrationClaim   `json:"claim,omitempty"`
}

func WorkerAction(action string) (string, bool) {
	switch action {
	case "status":
		return "integration_worker_status", true
	case "launch":
		return "integration_worker_launch", false
	case "fence":
		return "integration_worker_fence", false
	case "recover":
		return "integration_worker_recover", false
	case "abort":
		return "integration_worker_abort", false
	}
	return "", false
}
func WorkerInputValid(in IntegrationWorkerInput) error {
	name, readOnly := WorkerAction(in.Action)
	if name == "" || !executorCanaryID.MatchString(in.PermitID) || !integrationApprovalDigest.MatchString(in.ApprovalID) || (!readOnly && strings.TrimSpace(in.IdempotencyKey) == "") {
		return errors.New("integration worker requires fixed action, permit, approval and retry identities")
	}
	return nil
}
func (svc Service) WorkerCall(ctx context.Context, m *jobs.Manager, in IntegrationWorkerInput) (IntegrationWorkerOutput, error) {
	if err := WorkerInputValid(in); err != nil {
		return IntegrationWorkerOutput{}, err
	}
	a, err := ApprovalCall(ctx, m, IntegrationApprovalInput{PermitID: in.PermitID, ApprovalID: in.ApprovalID, Workdir: in.Workdir})
	if err != nil {
		return IntegrationWorkerOutput{}, err
	}
	return svc.WorkerWithApproval(ctx, m, in, a, func(ctx context.Context, action, permit, approval string) ([]byte, error) {
		return brokerclient.Run(ctx, "executor", action, permit, approval)
	})
}
func (svc Service) WorkerWithApproval(ctx context.Context, m *jobs.Manager, in IntegrationWorkerInput, a executor.IntegrationApproval, invoke func(context.Context, string, string, string) ([]byte, error)) (IntegrationWorkerOutput, error) {
	if err := WorkerInputValid(in); err != nil {
		return IntegrationWorkerOutput{}, err
	}
	_, readOnly := WorkerAction(in.Action)
	if a.ID != in.ApprovalID || a.TestPin.PermitID != in.PermitID || invoke == nil {
		return IntegrationWorkerOutput{}, errors.New("integration worker approval pins differ: HOLD")
	}
	if !readOnly && a.Request.Owner != svc.Owner(ctx) {
		return IntegrationWorkerOutput{}, errors.New("integration worker belongs to another authenticated owner: HOLD")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	action := "integration-worker-" + in.Action
	if in.Action == "launch" || in.Action == "abort" {
		action = "integration-" + in.Action
	}
	var raw []byte
	call := func() error { var err error; raw, err = invoke(ctx, action, in.PermitID, in.ApprovalID); return err }
	var err error
	if readOnly {
		err = call()
	} else {
		err = m.WithIdleProject(a.Request.Path, call)
	}
	if err != nil {
		return IntegrationWorkerOutput{}, err
	}
	return DecodeWorkerOutput(raw, in.Action, a)
}
func WorkerClaimValid(claim executor.IntegrationClaim, a executor.IntegrationApproval) bool {
	return claim.Version == 1 && claim.ApprovalID == a.ID && claim.Repository == a.Repository && claim.ExecutionID == a.ExecutionID && claim.Candidate == a.Request.Commit && claim.Owner == a.Request.Owner && claim.Generation > 0 && !claim.CreatedAt.IsZero() && ValidRecoveryState(claim.State, true)
}
func DecodeWorkerOutput(raw []byte, action string, a executor.IntegrationApproval) (IntegrationWorkerOutput, error) {
	if action == "abort" {
		var claim executor.IntegrationClaim
		if err := DecodeInput(string(raw), &claim); err != nil {
			return IntegrationWorkerOutput{}, err
		}
		if !WorkerClaimValid(claim, a) || claim.State != "FAILED" {
			return IntegrationWorkerOutput{}, errors.New("integration abort evidence pins differ: HOLD")
		}
		return IntegrationWorkerOutput{Claim: &claim}, nil
	}
	var state executor.IntegrationRuntime
	if err := DecodeInput(string(raw), &state); err != nil {
		return IntegrationWorkerOutput{}, err
	}
	launch := state.Launch
	if !WorkerClaimValid(state.Claim, a) || launch.Version != 1 || launch.State != "READY" || launch.ApprovalID != a.ID || !WorkerClaimValid(launch.Claim, a) || launch.Claim.Generation != state.Claim.Generation || launch.Pin.Generation != state.Claim.Generation || launch.Pin.ExecutionID != a.ExecutionID || !executorCanaryID.MatchString(launch.Pin.LeaseID) || !executorCanaryID.MatchString(launch.Pin.PermitID) || !executorCanaryID.MatchString(launch.LaunchID) || state.Worker.Pin != launch.Pin || len(state.Worker.Launches) != 1 || state.Worker.Launches[0].ID != launch.LaunchID {
		return IntegrationWorkerOutput{}, errors.New("integration worker evidence pins differ: HOLD")
	}
	if action == "launch" && !state.Dispatched {
		return IntegrationWorkerOutput{}, errors.New("integration worker dispatch not proven: HOLD")
	}
	if (action == "fence" || action == "recover") && state.Worker.State != "FENCED" {
		return IntegrationWorkerOutput{}, errors.New("integration worker fence not proven: HOLD")
	}
	return IntegrationWorkerOutput{Runtime: &state}, nil
}
func (svc Service) RegisterWorker(add func(tooldef.Def), m *jobs.Manager) {
	for _, action := range []string{"status", "launch", "fence", "recover", "abort"} {
		name, readOnly := WorkerAction(action)
		add(tooldef.New(&mcp.Tool{Name: name, Description: "Use a fixed root-sealed integration approval to " + action + " its worker. Scheduling receipts do not prove integration success; unknown dispatches are never relaunched.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationWorkerInput) (*mcp.CallToolResult, IntegrationWorkerOutput, error) {
			if in.Action != "" && in.Action != action {
				return nil, IntegrationWorkerOutput{}, errors.New("integration worker action differs from tool")
			}
			in.Action = action
			out, err := svc.WorkerCall(ctx, m, in)
			return nil, out, err
		}))
	}
}
func WorkerCompatInput(in ExecStartInput) (IntegrationWorkerInput, error) {
	if len(in.Args) != 2 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return IntegrationWorkerInput{}, errors.New("integration-worker requires one bounded JSON request and no overrides")
	}
	var v IntegrationWorkerInput
	if err := DecodeInput(in.Args[1], &v); err != nil {
		return v, err
	}
	if v.Workdir != in.Workdir || (v.IdempotencyKey != "" && v.IdempotencyKey != in.IdempotencyKey) {
		return v, errors.New("integration worker scope/retry pins differ")
	}
	v.IdempotencyKey = in.IdempotencyKey
	return v, WorkerInputValid(v)
}
func (svc Service) WorkerCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	v, err := WorkerCompatInput(in)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	name, readOnly := WorkerAction(v.Action)
	if err := svc.Authorize(ctx, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}); err != nil {
		return jobs.Snapshot{}, err
	}
	out, err := svc.WorkerCall(ctx, m, v)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	owner := sha256.Sum256([]byte(svc.Owner(ctx)))
	request := sha256.Sum256([]byte(in.Args[1]))
	return m.StoreReceipt(in.Command, []string{"integration-worker", "request-sha256:" + hex.EncodeToString(request[:]), "owner-sha256:" + hex.EncodeToString(owner[:])}, in.Workdir, string(raw))
}
