package mcpserver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GatewayProbeInput keeps changing internal schemas inside arguments.
type GatewayProbeInput struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}
type GatewayWaitInput struct {
	GatewayProbeInput
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"Bounded wait, 0..45 seconds; UNKNOWN and HOLD return immediately without replay."`
}
type GatewayObservation struct {
	ToolInvokeResult
	State         string    `json:"state"`
	ResourceState string    `json:"resource_state,omitempty"`
	CapturedAt    time.Time `json:"captured_at"`
}

func registerGatewayTools(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "gateway_status", Description: "Read an authorized internal status probe using its current schema. Refuses mutations and recursive gateways. Resource completion is not whole-task acceptance.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in GatewayProbeInput) (*mcp.CallToolResult, GatewayObservation, error) {
		out, err := r.gatewayObserve(ctx, in)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "gateway_wait", Description: "Wait at most 45 seconds on an authorized read-only probe. Returns UNKNOWN/HOLD without replay; continues an existing operation only by observation.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in GatewayWaitInput) (*mcp.CallToolResult, GatewayObservation, error) {
		out, err := r.gatewayWait(ctx, in)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "gateway_reconcile", Description: "Reconcile the ORIGINAL mutation tool, retry key and exact input through mutation_probe. Never executes, clears UNKNOWN or invents a new retry key. Recovery uses invoke with the live schema and original safety gates.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in MutationProbeInput) (*mcp.CallToolResult, ToolInvokeResult, error) {
		args, err := object(in)
		if err != nil {
			return nil, ToolInvokeResult{}, err
		}
		out, err := r.invoke(ctx, ToolInvokeInput{Name: "mutation_probe", Arguments: args})
		return nil, out, err
	})
}

func (r *toolRegistry) gatewayObserve(ctx context.Context, in GatewayProbeInput) (GatewayObservation, error) {
	out := GatewayObservation{State: "UNKNOWN", CapturedAt: time.Now().UTC()}
	if len(in.Name) > 128 || strings.HasPrefix(in.Name, "gateway_") || !r.lookupTool(in.Name).readOnly {
		return out, errors.New("gateway status/wait requires a registered read-only probe; use tool_resolve for its schema")
	}
	result, err := r.invoke(ctx, ToolInvokeInput{Name: in.Name, Arguments: in.Arguments})
	out.ToolInvokeResult = result
	if err != nil {
		return out, err
	}
	if result.IsError {
		message := strings.Join(result.Content, "; ")
		if len(message) > 4096 {
			message = strings.ToValidUTF8(message[:4096], "")
		}
		return out, errors.New("target probe returned an MCP error: " + message)
	}
	fields, err := object(result.Output)
	if err != nil {
		return out, err
	}
	// Some existing probes wrap their receipt in status/job/mission.
	for _, key := range []string{"status", "job", "mission"} {
		if nested, ok := fields[key].(map[string]any); ok {
			fields = nested
			break
		}
	}
	state, _ := fields["state"].(string)
	out.ResourceState = state
	switch strings.ToLower(state) {
	case "running", "queued", "pending", "reconciling", "cancelling", "executing", "verifying":
		out.State = "RUNNING"
	case "awaiting_face_id", "waiting_auth", "waiting", "authorized":
		out.State = "WAITING"
	case "hold", "blocked", "unknown":
		out.State = strings.ToUpper(state)
	case "done", "failed", "succeeded", "cancelled", "canceled", "expired", "rolled_back", "verify_failed":
		out.State = "TERMINAL"
	}
	return out, nil
}

func (r *toolRegistry) gatewayWait(ctx context.Context, in GatewayWaitInput) (GatewayObservation, error) {
	if in.TimeoutSeconds < 0 || in.TimeoutSeconds > 45 {
		return GatewayObservation{}, errors.New("timeout_seconds must be 0..45")
	}
	if in.TimeoutSeconds == 0 {
		return r.gatewayObserve(ctx, in.GatewayProbeInput)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var out GatewayObservation
	for {
		var err error
		out, err = r.gatewayObserve(ctx, in.GatewayProbeInput)
		if err != nil || out.State != "RUNNING" && out.State != "WAITING" {
			return out, err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return out, nil
			}
			return out, ctx.Err()
		case <-ticker.C:
		}
	}
}
