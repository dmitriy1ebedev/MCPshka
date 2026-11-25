package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tooldef"
)

type IntegrationPublishInput struct {
	PermitID       string `json:"permit_id"`
	ApprovalID     string `json:"approval_id"`
	Workdir        string `json:"workdir"`
	IdempotencyKey string `json:"idempotency_key"`
}

type IntegrationPublishResult struct {
	State     string `json:"state"`
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	Candidate string `json:"candidate"`
}

// The protected store is the integration authority; the working checkout
// follows it. Publication needs root's RELEASED claim for the exact approval,
// an idle project with no writer lease, and a pure fast-forward.
func (svc Service) RegisterPublish(add func(tooldef.Def), m *jobs.Manager, guard func(string, string) error, queue *integrationqueue.Runtime) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "integration_publish_main", Description: "After root records an integration RELEASED in the protected store, fast-forward the working checkout of the target branch (e.g. /root/repos/mcpshka main) to that exact candidate. Requires an idle project, no writer lease and a clean checkout at the admitted target head; repeating after success is a no-op. Never merges, rebases or deploys.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IntegrationPublishInput) (*mcp.CallToolResult, IntegrationPublishResult, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, IntegrationPublishResult{}, errors.New("retry key required")
		}
		a, err := ApprovalCall(ctx, m, IntegrationApprovalInput{PermitID: in.PermitID, ApprovalID: in.ApprovalID, Workdir: in.Workdir})
		if err != nil {
			return nil, IntegrationPublishResult{}, err
		}
		claim, err := svc.RecoveryCall(ctx, m, IntegrationRecoveryInput{PermitID: in.PermitID, ApprovalID: in.ApprovalID, Workdir: in.Workdir}, true)
		if err != nil {
			return nil, IntegrationPublishResult{}, err
		}
		if claim.State != "RELEASED" || claim.Candidate != a.Request.Commit {
			return nil, IntegrationPublishResult{}, errors.New("integration is not released by root: HOLD")
		}
		out := IntegrationPublishResult{Path: a.Request.Path, Branch: a.Request.TargetBranch, Candidate: a.Request.Commit}
		err = m.WithIdleProject(a.Request.Path, func() error {
			if err := guard(a.Request.Path, ""); err != nil {
				return err
			}
			state, err := integrationqueue.PublishFastForward(ctx, a.Request.Path, a.Request.TargetBranch, a.Request.TargetHead, a.Request.Commit)
			out.State = state
			return err
		})
		if err != nil {
			return nil, out, err
		}
		// The checkout is published; closing the queue item is bookkeeping and
		// is retried by repeating publish (ALREADY_PUBLISHED) if it fails.
		if queue != nil {
			if err := queue.MarkPublished(ctx, a.Request, a.ExecutionID); err != nil {
				return nil, out, fmt.Errorf("published; queue item not closed: %w", err)
			}
		}
		svc.Published(ctx, a)
		return nil, out, nil
	}))
}
