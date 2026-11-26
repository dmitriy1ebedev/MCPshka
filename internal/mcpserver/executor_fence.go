package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/jobs"
)

type ExecutorFenceInput struct {
	PermitID       string `json:"permit_id"`
	ApprovalID     string `json:"approval_id"`
	Workdir        string `json:"workdir"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type ExecutorFenceStatus struct {
	State    string          `json:"state"`
	Approval json.RawMessage `json:"approval"`
	Executor json.RawMessage `json:"executor"`
}

func executorFenceCall(ctx context.Context, m *jobs.Manager, in ExecutorFenceInput, status bool) (ExecutorFenceStatus, error) {
	if !executorCanaryID.MatchString(in.PermitID) || !executorCanaryID.MatchString(in.ApprovalID) || (!status && strings.TrimSpace(in.IdempotencyKey) == "") {
		return ExecutorFenceStatus{}, errors.New("executor fencing requires fixed permit, approval and mutation retry identities")
	}
	if _, err := m.Policy().CheckWorkdir(in.Workdir); err != nil {
		return ExecutorFenceStatus{}, err
	}
	action := "fence"
	if status {
		action = "fence-status"
	}
	out, err := brokerclient.Run(ctx, "executor", action, in.PermitID, in.ApprovalID)
	if err != nil {
		return ExecutorFenceStatus{}, err
	}
	if len(out) > 32768 {
		return ExecutorFenceStatus{}, errors.New("executor fencing output budget exceeded")
	}
	var result ExecutorFenceStatus
	if err = json.Unmarshal(out, &result); err != nil {
		return ExecutorFenceStatus{}, err
	}
	return result, nil
}

func registerExecutorFenceTools(r *toolRegistry, m *jobs.Manager) {
	registerIntegrationApprovalTool(r, m)
	addTool(r, &mcp.Tool{Name: "executor_fence_status", Description: "Inspect the root-issued administrative fencing approval and durable execution state. FENCING means an unresolved mutation, not proof of stopped writers.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in ExecutorFenceInput) (*mcp.CallToolResult, ExecutorFenceStatus, error) {
		out, err := executorFenceCall(ctx, m, in, true)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "executor_fence", Description: "Consume an existing root-issued one-use approval to fence the exact registered executor incarnation. Durable intent precedes stop; UNKNOWN is held without repeating stop. Does not transfer a project lease.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in ExecutorFenceInput) (*mcp.CallToolResult, ExecutorFenceStatus, error) {
		out, err := executorFenceCall(ctx, m, in, false)
		return nil, out, err
	})
}

func executorFenceCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 3 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("executor-fence requires permit, approval and no overrides")
	}
	if err := authorizeTool(ctx, &mcp.Tool{Name: "executor_fence", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}); err != nil {
		return jobs.Snapshot{}, err
	}
	out, err := executorFenceCall(ctx, m, ExecutorFenceInput{PermitID: in.Args[1], ApprovalID: in.Args[2], Workdir: in.Workdir, IdempotencyKey: in.IdempotencyKey}, false)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt("mcpshka", in.Args, in.Workdir, string(raw))
}
