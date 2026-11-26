package hostops

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/protectedcore"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/tooldef"
)

func RegisterSystem(add func(tooldef.Def), d Deps) {
	idempotencyStore := d.Store
	controlService := d.Control
	closed := false

	add(tooldef.New(&mcp.Tool{
		Name:        "protected_core_list",
		Description: "Return the machine-readable protected-core manifest: auth, policy, approvals, updater, rescue and the manifest itself. Listing does not activate or mutate protected core.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, ProtectedCoreResult, error) {
		return nil, ProtectedCoreResult{Components: protectedcore.Components()}, nil
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "device_status",
		Description: "Return a compact snapshot of the local Linux device: host, uptime, load, memory, disk and CPU temperature.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, systeminfo.Info, error) {
		info, err := systeminfo.Read()
		return nil, info, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "control_path_status",
		Description: "Return bounded control-plane survivability state: active primary MCP path, transport-only fallbacks, verified backups, LANE authority, rollback readiness and emergency state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, controlplane.Snapshot, error) {
		out, err := controlService.Snapshot(ctx)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "mutation_preflight",
		Description: "Assess control-plane blast radius before risky mutations. It blocks direct network writes on LANE-managed nodes, protects the only verified management path, requires verified cutover paths and enforces staged/timed rollback requirements.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in MutationPreflightInput) (*mcp.CallToolResult, controlplane.Preflight, error) {
		out, err := controlService.Preflight(ctx, controlplane.MutationRequest{
			Kind: in.Kind, ViaProvider: in.ViaProvider, AffectedPaths: in.AffectedPaths,
			Staged: in.Staged, RollbackSeconds: in.RollbackSeconds,
			NewPath: in.NewPath, DisableOldPath: in.DisableOldPath, Effective: in.Effective,
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "idempotency_status",
		Description: "Inspect durable PENDING, COMPLETED or FAILED state for a retry key. PENDING after an interrupted request is UNKNOWN outcome and must be reconciled rather than blindly replayed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in IdempotencyStatusInput) (*mcp.CallToolResult, idempotency.Status, error) {
		out, err := idempotencyStore.Lookup(in.Key)
		return nil, out, err
	}))
}
