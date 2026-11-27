package hostops

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/tooldef"
	"strings"
	"time"
)

func RegisterService(add func(tooldef.Def), d Deps) {
	manager := d.Manager
	idempotencyStore := d.Store
	systemdServices := d.Services
	closed := false
	nonDestructive := false

	add(tooldef.New(&mcp.Tool{
		Name:        "service_list",
		Description: "Return bounded structured status for all systemd services in the MCPshka read allowlist.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, systemdsvc.ListResult, error) {
		out, err := systemdServices.List(ctx)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "service_dependencies",
		Description: "Return a bounded dependency graph (Requires/Wants/After/Before) for an allowlisted systemd service.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ServiceStatusInput) (*mcp.CallToolResult, systemdsvc.DependencyGraph, error) {
		out, err := systemdServices.Dependencies(ctx, in.Service)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "service_status",
		Description: "Return bounded structured state for an allowlisted systemd service. Status is read directly with fixed systemctl properties.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ServiceStatusInput) (*mcp.CallToolResult, systemdsvc.Status, error) {
		out, err := systemdServices.Status(ctx, in.Service)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "journal_read",
		Description: "Read a bounded page from an allowlisted systemd journal. Initial reads use a capped lookback; follow-up reads use next_cursor.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JournalReadInput) (*mcp.CallToolResult, systemdsvc.JournalPage, error) {
		since := time.Duration(in.SinceSeconds) * time.Second
		out, err := systemdServices.JournalRead(ctx, in.Service, since, in.Lines, in.MaxBytes, in.Cursor)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "service_action",
		Description: "Start, stop or restart one explicitly mutation-allowlisted systemd service. MCPshka self-mutation remains reserved for guarded self-deploy. Requires idempotency and emits an audit event.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ServiceActionInput) (*mcp.CallToolResult, systemdsvc.Status, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, systemdsvc.Status{}, errors.New("idempotency_key is required for service_action")
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		payload := struct {
			Service string
			Action  string
		}{Service: in.Service, Action: action}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "service_action", payload, func() (systemdsvc.Status, error) {
			return systemdServices.Action(ctx, in.Service, action)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := action + "_ok"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "service_action", Result: result,
				Details: map[string]any{"service": in.Service, "action": action},
			})
		}
		return nil, out, err
	}))
}
