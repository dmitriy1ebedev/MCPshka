package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tools/integration"
)

type IntegrationShipInput = integration.IntegrationShipInput
type IntegrationShipResult = integration.IntegrationShipResult
type shipEnv = integration.Env

var defaultShipEnv = integration.DefaultEnv
var str = integration.Str

func registerIntegrationShip(r *toolRegistry) {
	closed := false
	tool := &mcp.Tool{
		Name:        "integration_ship",
		Description: "Move one commit through the whole integration conveyor with repeated calls: each call performs the next safe step (enqueue, sealed test prepare/launch/fence, then — after the explicit integration_approve review, whose approval_id you pass back — worker, recover, publish main) and returns stage, every ID and next_action. Same gates as calling those tools directly; retry keys are derived from commit and step, so repeat the same call after a lost reply, restart or wait. Never merges, rebases or deploys.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed},
	}
	captureCapability[IntegrationShipInput, IntegrationShipResult](r, tool)
	env := defaultShipEnv()
	handler := func(ctx context.Context, req *mcp.CallToolRequest, in IntegrationShipInput) (*mcp.CallToolResult, IntegrationShipResult, error) {
		out, err := r.ship(ctx, env, in)
		return nil, out, err
	}
	mcp.AddTool(r.server, tool, handler)
	r.catalogMu.Lock() // tool_invoke and the autorelease watcher call it by name (0.11.2 queue: «unknown tool»)
	r.invokers[tool.Name] = invokerFor(handler, tool)
	r.catalogMu.Unlock()
}

func (r *toolRegistry) call(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if run, ok := ctx.Value(shipRunKey{}).(*shipRun); ok {
		run.step.Store(name)
	}
	res, err := r.invoke(ctx, ToolInvokeInput{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	raw, err := json.Marshal(res.Output)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("%s: unexpected output", name)
		}
	}
	return out, nil
}

func (r *toolRegistry) canary(ctx context.Context, workdir, action, permit, launch, key string) (map[string]any, error) {
	return integration.Canary(ctx, r.call, workdir, action, permit, launch, key)
}
func (r *toolRegistry) shipStep(ctx context.Context, env shipEnv, in IntegrationShipInput) (IntegrationShipResult, error) {
	return integration.Step(ctx, env, in, r.call)
}
