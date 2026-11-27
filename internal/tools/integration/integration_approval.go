package integration

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/executor"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tooldef"
)

var executorCanaryID = regexp.MustCompile(`^[0-9a-f]{32}$`)

var integrationApprovalDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type IntegrationApprovalInput struct {
	PermitID   string `json:"permit_id"`
	ApprovalID string `json:"approval_id"`
	Workdir    string `json:"workdir"`
}

func ApprovalCall(ctx context.Context, m *jobs.Manager, in IntegrationApprovalInput) (executor.IntegrationApproval, error) {
	if !executorCanaryID.MatchString(in.PermitID) || !integrationApprovalDigest.MatchString(in.ApprovalID) {
		return executor.IntegrationApproval{}, errors.New("integration approval lookup requires fixed permit and approval identities")
	}
	root, err := gitops.New(m.Policy()).RepoRoot(in.Workdir)
	if err != nil {
		return executor.IntegrationApproval{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := brokerclient.Run(ctx, "executor", "integration-status", in.PermitID, in.ApprovalID)
	if err != nil {
		return executor.IntegrationApproval{}, err
	}
	if len(raw) > 16384 {
		return executor.IntegrationApproval{}, errors.New("integration approval output budget exceeded")
	}
	var a executor.IntegrationApproval
	if err := DecodeInput(string(raw), &a); err != nil {
		return executor.IntegrationApproval{}, err
	}
	if a.Version != 1 || a.ID != in.ApprovalID || a.TestPin.PermitID != in.PermitID || a.Request.Path != root || a.Source.Repo != root || a.Source.Commit != a.Request.Commit {
		return executor.IntegrationApproval{}, errors.New("integration approval pins differ: HOLD")
	}
	return a, nil
}

func RegisterApproval(add func(tooldef.Def), m *jobs.Manager) {
	add(tooldef.New(&mcp.Tool{Name: "integration_approval_status", Description: "Inspect a fixed root-issued candidate approval. Root revalidates sealed test manifest, fenced successful result and live common-repository identity. This read-only evidence does not acquire a writer claim or execute Git/deploy.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationApprovalInput) (*mcp.CallToolResult, executor.IntegrationApproval, error) {
		out, err := ApprovalCall(ctx, m, in)
		return nil, out, err
	}))
}
