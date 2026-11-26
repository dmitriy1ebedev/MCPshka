package integration

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/failure"
	"mcpshka/internal/gitprovider"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/tooldef"
)

type GitHubImportInput struct {
	Commit         string `json:"commit" jsonschema:"Full SHA of the GitHub source repository's current main."`
	SessionID      string `json:"admin_session_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type GitHubImportResult struct {
	Status integrationqueue.RuntimeStatus `json:"intent"`
	Next   failure.Action                 `json:"next_safe_action"`
}

func (svc Service) registerGitHubImport(add func(tooldef.Def), m *jobs.Manager, guard func(string, string) error, queue *integrationqueue.Runtime) {
	add(tooldef.New(&mcp.Tool{Name: "integration_import_github_main", Description: "Stage an external GitHub main merge of MCPshka for protected integration. Requires the owner's active Face ID session and a full GitHub main SHA. Fetches and admits only a fast-forward, then returns the next integration_ship step. Root independently verifies GitHub main during integration_approve and requires sealed evidence for the exact merge commit before the usual protected worker and checkout fast-forward.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in GitHubImportInput) (*mcp.CallToolResult, GitHubImportResult, error) {
		out, err := svc.importGitHubMain(ctx, m, guard, queue, in)
		return nil, out, err
	}))
}

func (svc Service) importGitHubMain(ctx context.Context, m *jobs.Manager, guard func(string, string) error, queue *integrationqueue.Runtime, in GitHubImportInput) (GitHubImportResult, error) {
	refuse := func(message string, err error) (GitHubImportResult, error) {
		return GitHubImportResult{}, failure.Rejected("GITHUB_IMPORT_REFUSED", "integration_import_github_main", message, err)
	}
	if !shipCommit.MatchString(in.Commit) || in.SessionID == "" || queue == nil {
		return refuse("full GitHub main SHA, active Face ID session and integration queue required", nil)
	}
	owner, err := svc.AdminOwner(ctx)
	if err != nil {
		return refuse("authenticated owner required", err)
	}
	session, err := svc.Admin(ctx, "status", in.SessionID, owner)
	if err != nil || Str(session, "id") != in.SessionID || Str(session, "owner") != owner || Str(session, "state") != "active" || Str(session, "auth_method") != "webauthn" {
		return refuse("active owner Face ID session required", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	repo := integrationqueue.GitHubImportRepo
	id, branch := integrationqueue.GitHubImportPins(in.Commit)
	status, err := queue.Status(ctx, repo, id)
	if err != nil {
		return refuse("cannot inspect integration intent", err)
	}
	if status.Found {
		if status.Item == nil || status.Item.Request.Owner != owner || status.Item.Request.Commit != in.Commit || status.Item.Request.Branch != branch || status.Item.State == integrationqueue.Cancelled {
			return refuse("existing import intent conflicts or was cancelled", nil)
		}
	} else {
		if err := guard(repo, ""); err != nil {
			return refuse("project writer unavailable", err)
		}
		if err := gitprovider.FetchGitHubMain(ctx, repo, in.Commit); err != nil {
			return refuse("GitHub main objects unavailable", err)
		}
		pin, err := roadmapscan.Load(filepath.Join(repo, "ROADMAP.md"), "")
		if err != nil {
			return refuse("ROADMAP unavailable", err)
		}
		err = m.WithIdleProject(repo, func() error {
			if err := guard(repo, ""); err != nil {
				return err
			}
			plan, err := integrationqueue.PrepareGitHubImport(ctx, repo, in.Commit)
			if err != nil {
				return err
			}
			status, err = queue.Enqueue(ctx, integrationqueue.Request{Owner: owner, Path: repo, ID: id, Branch: branch, Commit: in.Commit, TargetBranch: "main", TargetHead: plan.TargetHead, RoadmapSHA: pin.SHA256, Scope: "P7"})
			return err
		})
		if err != nil {
			return GitHubImportResult{}, err
		}
	}
	if !status.Found || status.Item == nil {
		return GitHubImportResult{}, errors.New("integration intent is unverified: HOLD")
	}
	return GitHubImportResult{Status: status, Next: failure.Action{Tool: "integration_ship", Arguments: map[string]any{"path": repo, "branch": branch, "commit": in.Commit, "id": id, "evidence": "auto", "admin_session_id": in.SessionID}}}, nil
}
