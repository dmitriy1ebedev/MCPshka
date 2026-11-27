package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/haprovider"
	"mcpshka/internal/idempotency"
)

type HAStatesInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"Optional entity domain, e.g. light, switch, sensor."`
	Query  string `json:"query,omitempty" jsonschema:"Optional case-insensitive substring of entity id or friendly name."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum entities (default 100, cap 500)."`
}

type HAStateInput struct {
	EntityID string `json:"entity_id" jsonschema:"Entity id, e.g. light.kitchen."`
}

type HAServiceCallInput struct {
	Domain         string         `json:"domain" jsonschema:"Service domain, e.g. light, automation, script, input_boolean."`
	Service        string         `json:"service" jsonschema:"Service name, e.g. turn_on, reload, trigger."`
	Data           map[string]any `json:"data,omitempty" jsonschema:"Service data, e.g. {\"entity_id\":\"light.kitchen\"}."`
	IdempotencyKey string         `json:"idempotency_key" jsonschema:"Required retry key."`
}

func registerHATools(registry *toolRegistry, store *idempotency.Store) {
	ha := haprovider.New()
	closed := false
	destructive := false
	addTool(registry, &mcp.Tool{
		Name:        "ha_states",
		Description: "Home Assistant entities filtered by domain and/or text (id or friendly name), without attributes. Read-only, bounded.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HAStatesInput) (*mcp.CallToolResult, haprovider.StatesResult, error) {
		out, err := ha.States(ctx, in.Domain, in.Query, in.Limit)
		return nil, out, err
	})
	addTool(registry, &mcp.Tool{
		Name:        "ha_state",
		Description: "One Home Assistant entity with attributes. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HAStateInput) (*mcp.CallToolResult, haprovider.State, error) {
		out, err := ha.State(ctx, in.EntityID)
		return nil, out, err
	})
	addTool(registry, &mcp.Tool{
		Name:        "ha_check_config",
		Description: "Validate Home Assistant configuration without applying it. Run before any reload after editing YAML.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, haprovider.CheckResult, error) {
		out, err := ha.CheckConfig(ctx)
		return nil, out, err
	})
	addTool(registry, &mcp.Tool{
		Name:        "ha_service_call",
		Description: "Call one Home Assistant service (turn_on/off, automation.trigger, <domain>.reload, homeassistant.reload_core_config…). Stop/restart of Home Assistant itself is refused. Check ha_state before and after.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HAServiceCallInput) (*mcp.CallToolResult, haprovider.CallResult, error) {
		out, err := runIdempotent(store, in.IdempotencyKey, "ha_service_call", in, func() (haprovider.CallResult, error) {
			return ha.CallService(ctx, in.Domain, in.Service, in.Data)
		})
		return nil, out, err
	})
}
