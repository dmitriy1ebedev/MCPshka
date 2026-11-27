package hostops

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/dockerlogs"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
	"strings"
)

func RegisterDocker(add func(tooldef.Def), d Deps) {
	manager := d.Manager
	idempotencyStore := d.Store
	dockerService := d.Docker
	closed := false
	nonDestructive := false

	add(tooldef.New(&mcp.Tool{
		Name:        "docker_list",
		Description: "List Docker containers with bounded entries and compact state fields.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerListInput) (*mcp.CallToolResult, dockerops.ListResult, error) {
		out, err := dockerService.List(ctx, in.MaxEntries)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "docker_status",
		Description: "Return compact Docker state and health for one validated container name.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerContainerInput) (*mcp.CallToolResult, dockerops.Status, error) {
		out, err := dockerService.Status(ctx, in.Container)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "docker_inspect",
		Description: "Return bounded container metadata including restart policy, Compose project/service labels, networks and mount destinations.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerContainerInput) (*mcp.CallToolResult, dockerops.Inspect, error) {
		out, err := dockerService.Inspect(ctx, in.Container)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "docker_resources",
		Description: "Return one bounded no-stream Docker resource snapshot for a validated container.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerContainerInput) (*mcp.CallToolResult, dockerops.Resources, error) {
		out, err := dockerService.Resources(ctx, in.Container)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "docker_logs",
		Description: "Read a bounded Docker log page with since/cursor continuation, component and level filters. Oversized records are truncated, never included in tool errors.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerLogsInput) (*mcp.CallToolResult, dockerops.Logs, error) {
		out, err := dockerService.LogsPage(ctx, dockerlogs.Query{Container: in.Container, SinceSeconds: in.SinceSeconds, Since: in.Since, Cursor: in.Cursor, Component: in.Component, Level: in.Level, Lines: in.Lines, MaxBytes: in.MaxBytes})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "docker_action",
		Description: "Start, stop or restart one explicitly mutation-allowlisted Docker container. Mutations are serialized, destructive operations such as remove/kill are not exposed, and idempotency is required.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerActionInput) (*mcp.CallToolResult, dockerops.Status, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, dockerops.Status{}, errors.New("idempotency_key is required for docker_action")
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		payload := struct {
			Container string
			Action    string
		}{Container: in.Container, Action: action}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "docker_action", payload, func() (dockerops.Status, error) {
			return dockerService.Action(ctx, in.Container, action)
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := action + "_ok"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "docker_action", Result: result,
				Details: map[string]any{"container": in.Container, "action": action},
			})
		}
		return nil, out, err
	}))
}
