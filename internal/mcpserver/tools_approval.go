package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/approval"
	"mcpshka/internal/policy"
	"strings"
	"time"
)

func registerApprovalTools(registry *toolRegistry, d *serverDeps) {
	manager := d.manager
	approvalStore := d.approvalStore
	closed := false
	nonDestructive := false

	addTool(registry, &mcp.Tool{
		Name:        "exec_classify",
		Description: "Classify a command as read_only, mutating, destructive or privileged and report whether current policy allows it. This is risk routing, not a sandbox.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ExecClassifyInput) (*mcp.CallToolResult, policy.CommandDecision, error) {
		var (
			decision policy.CommandDecision
			err      error
		)
		if strings.TrimSpace(in.Workdir) == "" {
			decision, err = manager.Policy().DecideCommand(in.Command, in.Args)
		} else {
			cwd, resolveErr := manager.Policy().CheckWorkdir(in.Workdir)
			if resolveErr != nil {
				return nil, decision, resolveErr
			}
			decision, err = manager.Policy().DecideCommandForWorkdir(in.Command, in.Args, cwd)
		}
		if err != nil && decision.Class == "" {
			return nil, decision, err
		}
		return nil, decision, nil
	})

	addTool(registry, &mcp.Tool{
		Name:        "approval_request",
		Description: "Create a pending one-time approval request for an exact destructive command, arguments, canonical workdir and trusted plan SHA-256. This never grants the approval.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ApprovalRequestInput) (*mcp.CallToolResult, approval.Request, error) {
		cwd, err := manager.Policy().CheckWorkdir(in.Workdir)
		if err != nil {
			return nil, approval.Request{}, err
		}
		decision, policyErr := manager.Policy().DecideCommandForWorkdir(in.Command, in.Args, cwd)
		if decision.Class != policy.ClassDestructive {
			return nil, approval.Request{}, fmt.Errorf("approval_request only accepts destructive operations, got %s", decision.Class)
		}
		if decision.Reason == "explicit denylist" {
			return nil, approval.Request{}, fmt.Errorf("explicitly denied command cannot be approved")
		}
		if decision.Profile != policy.ProfileOperator {
			return nil, approval.Request{}, fmt.Errorf("destructive approvals require operator profile")
		}
		if policyErr == nil {
			return nil, approval.Request{}, fmt.Errorf("destructive operation is already allowed and does not need approval")
		}
		if strings.TrimSpace(in.PlanHash) == "" {
			return nil, approval.Request{}, errors.New("plan_hash is required for approval_request")
		}
		ttl := time.Duration(in.TTLSeconds) * time.Second
		if in.TTLSeconds <= 0 {
			ttl = approval.DefaultTTL
		}
		out, err := approvalStore.RequestWithPlan(in.Command, in.Args, cwd, in.PlanHash, ttl)
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "approval_status",
		Description: "Inspect a destructive approval request without exposing its secret token.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ApprovalStatusInput) (*mcp.CallToolResult, approval.Request, error) {
		out, err := approvalStore.Status(in.ApprovalID)
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "approval_gate_status",
		Description: "Return the workflow-facing approval gate state. Pending waits for approval, expired becomes HOLD without mutation, granted becomes READY, and consumed becomes COMPLETED. Includes severity and a stable dedup key without exposing the approval secret.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ApprovalStatusInput) (*mcp.CallToolResult, approval.GateStatus, error) {
		out, err := approvalStore.Gate(in.ApprovalID)
		return nil, out, err
	})
}
