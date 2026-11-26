package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/toolforge"
)

func TestInvokerHonorsAdvertisedInputConstraints(t *testing.T) {
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "schema", Version: "1"}, nil), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	calls := 0
	schema := map[string]any{"type": "object", "properties": map[string]any{"choice": map[string]any{"type": "string", "enum": []string{"accepted"}}}, "required": []string{"choice"}, "additionalProperties": false}
	addTool(r, &mcp.Tool{Name: "fixture_schema", InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		calls++
		return nil, map[string]any{"ok": true}, nil
	})
	for _, args := range []map[string]any{{}, {"choice": "rejected"}, {"choice": "accepted", "extra": true}} {
		if _, _, err := r.invokers["fixture_schema"](context.Background(), args); err == nil {
			t.Fatal("advertised constraint bypassed", args)
		}
	}
	if calls != 0 {
		t.Fatal("invalid call reached handler", calls)
	}
	if _, _, err := r.invokers["fixture_schema"](context.Background(), map[string]any{"choice": "accepted"}); err != nil || calls != 1 {
		t.Fatal("valid schema call rejected", calls, err)
	}
}

func TestCompositeStopsOnMCPError(t *testing.T) {
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "composite", Version: "1"}, nil), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	addTool(r, &mcp.Tool{Name: "fixture_first", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "executor refused"}}}, map[string]any{"reason": "refused"}, nil
	})
	calls := 0
	addTool(r, &mcp.Tool{Name: "fixture_next", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		calls++
		return nil, map[string]any{"ok": true}, nil
	})
	r.registerComposite(toolforge.Composite{Name: "x_fixture_stop", ReadOnly: true, Steps: []toolforge.Step{{Tool: "fixture_first"}, {Tool: "fixture_next"}}})
	res, out, err := r.invokers["x_fixture_stop"](context.Background(), map[string]any{})
	if err != nil || res == nil || !res.IsError || calls != 0 {
		t.Fatalf("chain continued after refusal: calls=%d res=%+v out=%+v err=%v", calls, res, out, err)
	}
}

func TestReadProbeRejectsMCPError(t *testing.T) {
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "read-refusal", Version: "1"}, nil), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	addTool(r, &mcp.Tool{Name: "fixture_refusal", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "backend refused"}}}, map[string]any{"untrusted": "value"}, nil
	})
	if _, err := r.readers["fixture_refusal"](context.Background(), map[string]any{}); err == nil {
		t.Error("MCP refusal became a successful before/after probe")
	}
	root := t.TempDir()
	p := policy.Default()
	p.AllowedWorkdirs = []string{root}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	r.receipts = manager
	if _, err := r.toolInvokeCompat(context.Background(), map[string]any{"command": "mcpshka", "args": []string{"tool-invoke", "fixture_refusal", "{}"}, "workdir": root, "idempotency_key": "refusal"}); err == nil {
		t.Fatal("MCP refusal became a successful compatibility job")
	}
}

func TestCompositePartialEffectCannotInheritChildNoEffect(t *testing.T) {
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "partial", Version: "1"}, nil), store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	for _, name := range []string{"service_status", "state_status"} {
		r.readers[name] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	}
	calls := 0
	addTool(r, &mcp.Tool{Name: "service_action"}, func(_ context.Context, _ *mcp.CallToolRequest, in ServiceActionInput) (*mcp.CallToolResult, handlerOutput, error) {
		if in.Service != "test.service" {
			t.Fatal("dynamic input lost", in)
		}
		calls++
		return nil, handlerOutput{Value: "applied"}, nil
	})
	addTool(r, &mcp.Tool{Name: "fixture_rejected", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		return nil, nil, failure.Rejected("FIXTURE", "test", "rejected before this step", nil)
	})
	r.registerComposite(toolforge.Composite{Name: "x_fixture_partial", Inputs: []string{"target_service"}, Steps: []toolforge.Step{{Tool: "service_action", Args: map[string]any{"service": "{{input.target_service}}", "action": "restart"}}, {Tool: "fixture_rejected"}}})
	_, _, err := r.invokers["x_fixture_partial"](context.Background(), map[string]any{"idempotency_key": "partial", "target_service": "test.service", "private_note": "fixture-private-input"})
	if err != nil && strings.Contains(err.Error(), "fixture-private-input") {
		t.Error("recovery action exposes original private arguments", err)
	}
	if err == nil || failure.IsNoEffect(err) || calls != 1 {
		t.Fatalf("partial effect lost: calls=%d err=%v", calls, err)
	}
	_, _, err = r.invokers["x_fixture_partial"](context.Background(), map[string]any{"idempotency_key": "partial", "target_service": "test.service", "private_note": "fixture-private-input"})
	if err == nil || calls != 1 {
		t.Fatal("partial chain replayed instead of reconciliation", calls, err)
	}
}

func TestMutationReplayKeepsMCPError(t *testing.T) {
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "error-replay", Version: "1"}, nil), store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{"service_status": func(context.Context, map[string]any) (any, error) {
		return map[string]any{"active_state": "active"}, nil
	}}, tools: map[string]bool{}}
	calls := 0
	addTool(r, &mcp.Tool{Name: "service_action"}, func(context.Context, *mcp.CallToolRequest, ServiceActionInput) (*mcp.CallToolResult, handlerOutput, error) {
		calls++
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "executor refused"}}}, handlerOutput{Value: "refused"}, nil
	})
	for i := 0; i < 2; i++ {
		res, _, err := r.invokers["service_action"](context.Background(), map[string]any{"service": "test.service", "action": "restart", "idempotency_key": "same-refusal"})
		if err != nil || res == nil || !res.IsError {
			t.Fatalf("attempt %d lost MCP refusal: %+v %v", i, res, err)
		}
		text := ""
		for _, c := range res.Content {
			if c, ok := c.(*mcp.TextContent); ok {
				text += c.Text
			}
		}
		if !strings.Contains(text, "executor refused") {
			t.Fatalf("attempt %d lost refusal reason", i)
		}
	}
	if calls != 1 {
		t.Fatalf("replayed executor: %d", calls)
	}
}
