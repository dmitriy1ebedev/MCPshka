package mcpserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/deploy"
	"strings"
)

func registerDeployTools(registry *toolRegistry, d *serverDeps) {
	manager := d.manager
	deploys := d.deploys
	idempotencyStore := d.idempotencyStore
	closed := false
	destructive := true

	addTool(registry, &mcp.Tool{
		Name:        "project_deploy",
		Description: "Schedule a guarded MCPshka self-deployment from an authenticated operator chat workflow. Signed admission requires the SHA-named candidate plus .release-plan.json and .ci-receipt.json companions prepared after independent CI verification; see docs/RELEASE_PROMOTION.md. The detached fixed root helper preserves rollback, verifies exact hashes and health/tunnel/DNS/HTTPS, and rolls back on failure. idempotency_key is required.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectDeployInput) (*mcp.CallToolResult, deploy.Result, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, deploy.Result{}, errors.New("idempotency_key is required for project_deploy")
		}
		payload := struct {
			Path            string
			CandidatePath   string
			ExpectedVersion string
		}{in.Path, in.CandidatePath, in.ExpectedVersion}
		out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, reservedDeployOperation, payload, func() (deploy.Result, error) {
			receipt, err := idempotencyStore.Lookup(in.IdempotencyKey)
			if err != nil {
				return deploy.Result{}, err
			}
			id, err := reservedDeploymentID(in.IdempotencyKey, receipt)
			if err != nil {
				return deploy.Result{}, err
			}
			return deploys.StartReserved(ctx, in.Path, in.CandidatePath, in.ExpectedVersion, id)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "accepted"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "project_deploy", Workdir: in.Path, Result: result,
				Details: map[string]any{
					"deployment_id":  out.DeploymentID,
					"target_version": in.ExpectedVersion,
				},
			})
		}
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "broker_install",
		Description: "Schedule the pinned MCPshka privileged-broker installer through a detached transient unit. No arbitrary privileged command or arguments are exposed. idempotency_key is required.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in BrokerInstallInput) (*mcp.CallToolResult, deploy.BrokerInstallResult, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, deploy.BrokerInstallResult{}, errors.New("idempotency_key is required for broker_install")
		}
		payload := struct {
			Path string
		}{Path: in.Path}
		out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, "broker_install", payload, func() (deploy.BrokerInstallResult, error) {
			return deploys.InstallBroker(ctx, in.Path)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "accepted"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "broker_install", Workdir: in.Path, Result: result,
				Details: map[string]any{"operation_id": out.OperationID, "unit": out.Unit},
			})
		}
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "service_user_migrate",
		Description: "Schedule the pinned MCPshka root-to-service-user migration through a detached guarded transient unit. The helper verifies broker/auth/control-path prerequisites and rolls the service unit back on failed health verification. idempotency_key is required.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ServiceUserMigrateInput) (*mcp.CallToolResult, deploy.ServiceUserMigrationResult, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, deploy.ServiceUserMigrationResult{}, errors.New("idempotency_key is required for service_user_migrate")
		}
		payload := struct {
			Path string
		}{Path: in.Path}
		out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, "service_user_migrate", payload, func() (deploy.ServiceUserMigrationResult, error) {
			return deploys.MigrateServiceUser(ctx, in.Path)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "accepted"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "service_user_migrate", Workdir: in.Path, Result: result,
				Details: map[string]any{"operation_id": out.OperationID, "unit": out.Unit, "status_path": out.StatusPath},
			})
		}
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "project_deploy_reconcile",
		Description: "Read bounded durable deployment evidence, correlation IDs and stale/UNKNOWN blockers after restart or lost replies. Never rewrites terminal state, retries, deploys or grants authority. Pending remains HOLD until independently reconciled.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, deploy.Reconciliation, error) {
		out, err := deploys.Reconcile()
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "project_deploy_status",
		Description: "Read the durable bounded status of a deployment previously scheduled by project_deploy.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectDeployStatusInput) (*mcp.CallToolResult, deploy.Status, error) {
		out, err := deploys.Status(in.DeploymentID)
		return nil, out, err
	})
}
