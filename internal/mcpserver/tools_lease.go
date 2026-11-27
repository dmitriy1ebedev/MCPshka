package mcpserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/roadmaprun"
	"path/filepath"
	"strings"
)

func registerLeaseTools(registry *toolRegistry, d *serverDeps) {
	manager := d.manager
	leases := d.leases
	pipelines := d.pipelines
	idempotencyStore := d.idempotencyStore
	closed := false
	nonDestructive := false

	addTool(registry, &mcp.Tool{
		Name:        "project_lease_acquire",
		Description: "Atomically acquire the single-writer lease for a project's canonical Git root. Returns a secret token required for release.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectLeaseAcquireInput) (*mcp.CallToolResult, projectlease.AcquireResult, error) {
		out, err := acquireProjectLease(ctx, d, in)
		if err == nil {
			current, checkErr := leases.ValidateHolder(in.Path, out.Token)
			if checkErr != nil {
				return nil, projectlease.AcquireResult{}, checkErr
			}
			out.Status = current
		}
		if logger := manager.AuditLogger(); logger != nil {
			result := "acquired"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{Type: "project_lease_acquire", Workdir: in.Path, Result: result, Details: map[string]any{"owner": in.Owner}})
		}
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "project_lease_status",
		Description: "Inspect the single-writer lease for a project's canonical Git root without exposing the secret release token.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectLeaseStatusInput) (*mcp.CallToolResult, ProjectLeaseRecoveryStatus, error) {
		out, err := leases.Status(in.Path)
		if err != nil {
			return nil, ProjectLeaseRecoveryStatus{}, err
		}
		claim, claimErr := pipelines.ProjectClaim(out.ProjectRoot)
		if manager.Database() == nil {
			claimErr = errors.New("SQLite authority unavailable")
		}
		activity := manager.ProjectActivity(out.ProjectRoot, 16)
		status := leaseSchedulingStatus(out, activity, claim, claimErr)
		if claimErr == nil && out.Held {
			gone, reason := leaseHolderGone(out, func(id string) bool {
				_, err := manager.Get(id)
				return err != nil && strings.Contains(err.Error(), "not found")
			}, func(id string) string {
				if !validRoadmapRunID(id) {
					return ""
				}
				j, err := roadmaprun.ReadJournal(filepath.Join(roadmaprun.StateDir, id+".json"), hostBootTime())
				if err != nil {
					return ""
				}
				return j.State
			})
			status = markHolderGone(status, out, activity, claim, gone, reason)
		}
		return nil, status, nil
	})

	addTool(registry, &mcp.Tool{Name: "project_lease_resume", Description: "Voluntary idle-worktree handoff using the current holder token. Rotates token/execution and generation atomically with an audit journal; preserves all files. Same key replays the same successor. Owner-family names grant no authority.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in projectlease.ResumeInput) (*mcp.CallToolResult, projectlease.AcquireResult, error) {
		in.IdempotencyKey = mutationReceiptKey(ctx, "project_lease_resume", in.IdempotencyKey, nil)
		out, err := leases.Resume(in, manager, d.deploys, pipelines.Guard)
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "project_lease_admin_review",
		Description: "Inspect administrative takeover prerequisites without changing writer ownership. A review is not an authorization or a transfer receipt.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectLeaseStatusInput) (*mcp.CallToolResult, projectlease.AdministrativeReview, error) {
		out, err := leases.ReviewAdministrativeTransfer(in.Path)
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "project_lease_heartbeat",
		Description: "Renew a tracked SQLite project lease with its holder token. Legacy leases cannot be auto-recovered. Use a NEW retry key per heartbeat.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectLeaseHeartbeatInput) (*mcp.CallToolResult, projectlease.Status, error) {
		payload := struct{ Path, Token string }{in.Path, in.Token}
		out, err := runIdempotent(idempotencyStore, mutationReceiptKey(ctx, "project_lease_heartbeat", in.IdempotencyKey, nil), "project_lease_heartbeat", payload, func() (projectlease.Status, error) {
			return leases.HeartbeatRefresh(in.Path, in.Token)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "heartbeat_ok"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{Type: "project_lease_heartbeat", Workdir: in.Path, Result: result})
		}
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "project_lease_release",
		Description: "Release only the current holder's lease after native jobs/resources and deployment are verified idle. Active or UNKNOWN jobs keep ownership; wait on their original IDs.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectLeaseReleaseInput) (*mcp.CallToolResult, projectlease.ReleaseResult, error) {
		payload := struct {
			Path  string
			Token string
		}{in.Path, in.Token}
		out, err := runIdempotent(idempotencyStore, mutationReceiptKey(ctx, "project_lease_release", in.IdempotencyKey, nil), "project_lease_release", payload, func() (projectlease.ReleaseResult, error) {
			return leases.ReleaseIdle(in.Path, in.Token, manager, d.deploys)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "released"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{Type: "project_lease_release", Workdir: in.Path, Result: result})
		}
		return nil, out, err
	})
}
