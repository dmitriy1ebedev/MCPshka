package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"strings"
)

type IntegrationEnqueueInput struct {
	Path           string `json:"path"`
	ID             string `json:"id"`
	Branch         string `json:"branch"`
	Commit         string `json:"commit"`
	TargetBranch   string `json:"target_branch"`
	TargetHead     string `json:"target_head"`
	RoadmapSHA     string `json:"roadmap_sha256"`
	OwnedBranchID  string `json:"owned_branch_id,omitempty"`
	Scope          string `json:"scope"`
	IdempotencyKey string `json:"idempotency_key"`
}
type IntegrationStatusInput struct {
	Path string `json:"path"`
	ID   string `json:"id,omitempty"`
}
type IntegrationCancelInput struct {
	Path           string `json:"path"`
	ID             string `json:"id"`
	Commit         string `json:"commit"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (svc Service) RegisterQueue(add func(tooldef.Def), e *integrationqueue.Runtime) {
	add(tooldef.New(&mcp.Tool{Name: "integration_status", Description: "Read SQLite integration intent pinned to a common Git repository. UNTRUSTED receipts do not approve tests, review, merging or deployment.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationStatusInput) (*mcp.CallToolResult, integrationqueue.RuntimeStatus, error) {
		out, err := e.Status(ctx, in.Path, in.ID)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "integration_enqueue", Description: "Durably admit an authenticated owner's fixed commit, target HEAD and ROADMAP scope. Atomic bounded admission shared by linked worktrees; no Git mutation or automatic approval.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationEnqueueInput) (*mcp.CallToolResult, integrationqueue.RuntimeStatus, error) {
		out, err := e.Enqueue(ctx, integrationqueue.Request{Owner: svc.Owner(ctx), Path: in.Path, ID: in.ID, Branch: in.Branch, Commit: in.Commit, TargetBranch: in.TargetBranch, TargetHead: in.TargetHead, RoadmapSHA: in.RoadmapSHA, Scope: in.Scope, OwnedBranchID: in.OwnedBranchID})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "integration_cancel", Description: "Cancel the authenticated owner's pending integration intent using its exact commit. Idempotent cancellation never changes Git.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationCancelInput) (*mcp.CallToolResult, integrationqueue.RuntimeStatus, error) {
		out, err := e.CancelAs(ctx, in.Path, in.ID, in.Commit, svc.Owner(ctx))
		return nil, out, err
	}))
}

func IsMutation(name string, input map[string]any) bool {
	if name == "integration_worker_launch" || name == "integration_worker_fence" || name == "integration_worker_recover" || name == "integration_worker_abort" {
		return true
	}
	if name == "integration_enqueue" || name == "integration_cancel" || name == "integration_recover" {
		return true
	}
	if name != "exec_start" {
		return false
	}
	command, _ := input["command"].(string)
	args, _ := input["args"].([]any)
	return (command == "mcpshka" || command == "/usr/local/bin/mcpshka") && len(args) > 0 && (args[0] == "integration" || args[0] == "integration-recover" || args[0] == "integration-worker")
}

func DecodeInput(raw string, out any) error {
	if len(raw) > 16384 {
		return errors.New("integration input budget exceeded")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("integration input requires one JSON object")
	}
	return nil
}

func (svc Service) Compat(ctx context.Context, e *integrationqueue.Runtime, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || len(in.Args) < 2 {
		return jobs.Snapshot{}, errors.New("integration overrides forbidden")
	}
	action := in.Args[1]
	if action != "enqueue" && action != "status" && action != "cancel" {
		return jobs.Snapshot{}, errors.New("integration action requires enqueue/status/cancel")
	}
	if err := svc.Authorize(ctx, &mcp.Tool{Name: "integration_" + action, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: action == "status"}}); err != nil {
		return jobs.Snapshot{}, err
	}
	var out integrationqueue.RuntimeStatus
	var err error
	switch {
	case action == "enqueue" && len(in.Args) == 3:
		var v IntegrationEnqueueInput
		if err = DecodeInput(in.Args[2], &v); err == nil {
			out, err = e.Enqueue(ctx, integrationqueue.Request{Owner: svc.Owner(ctx), Path: v.Path, ID: v.ID, Branch: v.Branch, Commit: v.Commit, TargetBranch: v.TargetBranch, TargetHead: v.TargetHead, RoadmapSHA: v.RoadmapSHA, Scope: v.Scope, OwnedBranchID: v.OwnedBranchID})
		}
	case action == "cancel" && len(in.Args) == 3:
		var v IntegrationCancelInput
		if err = DecodeInput(in.Args[2], &v); err == nil {
			out, err = e.CancelAs(ctx, v.Path, v.ID, v.Commit, svc.Owner(ctx))
		}
	case action == "status" && len(in.Args) == 4:
		out, err = e.Status(ctx, in.Args[2], in.Args[3])
	default:
		err = errors.New("integration requires enqueue JSON, cancel JSON, or status PATH ID")
	}
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	owner := sha256.Sum256([]byte(svc.Owner(ctx)))
	request := sha256.Sum256([]byte(strings.Join(in.Args, "\x00")))
	args := []string{"integration", action, "request-sha256:" + hex.EncodeToString(request[:]), "owner-sha256:" + hex.EncodeToString(owner[:])}
	return m.StoreReceipt(in.Command, args, in.Workdir, string(raw))
}

type ExecStartInput = toolcontract.ExecStartInput
