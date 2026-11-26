package tooldef

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/failure"
)

// Apply the error envelope after SDK validation on both MCP surfaces.
func FailureMetadata(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if method != "tools/call" || err != nil {
			return result, err
		}
		out, ok := result.(*mcp.CallToolResult)
		if !ok || out == nil {
			return result, err
		}
		proof := failure.ForWire(out.GetError())
		if !out.IsError && out.Meta[failure.MetadataKey] == nil {
			return result, err
		}
		copy := *out
		copy.Meta = maps.Clone(out.Meta)
		delete(copy.Meta, failure.MetadataKey)
		if out.IsError && proof != nil {
			if copy.Meta == nil {
				copy.Meta = mcp.Meta{}
			}
			copy.Meta[failure.MetadataKey] = proof
		}
		if out.IsError {
			copy.StructuredContent = toolFailureEnvelope(out, request)
		}
		return &copy, nil
	}
}

func toolFailureEnvelope(out *mcp.CallToolResult, request mcp.Request) failure.Envelope {
	text := MCPErrorText(out)
	err := out.GetError()
	schemaRefusal := err != nil && (strings.HasPrefix(err.Error(), `validating "arguments":`) ||
		strings.HasPrefix(err.Error(), "arguments do not match the tool schema:") ||
		strings.HasPrefix(err.Error(), "applying tool schema defaults:"))
	if schemaRefusal {
		err = failure.Rejected("INVALID_ARGUMENT", "mcp.arguments", text, nil)
		failure.Details(err).NextSafeAction = failure.Action{Tool: "tool_resolve", Arguments: map[string]any{}}
	}
	envelope := failure.ToolEnvelope(failure.Classify(err, nil), text, schemaRefusal)
	if request == nil {
		return envelope
	}
	params, _ := request.GetParams().(*mcp.CallToolParamsRaw)
	if params == nil {
		return envelope
	}
	var args struct {
		Name      string          `json:"name"`
		TaskID    *string         `json:"task_id"`
		JobID     *string         `json:"job_id"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params.Arguments, &args)
	name := params.Name
	if name == "tool_invoke" && len(args.Arguments) > 0 {
		name = args.Name
		args.TaskID, args.JobID = nil, nil
		_ = json.Unmarshal(args.Arguments, &args)
	}
	if schemaRefusal {
		envelope.NextSafeAction.Arguments["goal"] = name
	}
	if envelope.TaskID == nil {
		envelope.TaskID = boundedHandle(args.TaskID)
	}
	if envelope.JobID == nil {
		envelope.JobID = boundedHandle(args.JobID)
	}
	return envelope
}

func boundedHandle(id *string) *string {
	if id == nil || len(*id) == 0 || len(*id) > 128 {
		return nil
	}
	return id
}

// Error receipts keep a bounded diagnostic; arbitrary result content stays private.
func MCPErrorText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range result.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok || text == nil || text.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text.Text[:min(len(text.Text), 2048-b.Len())])
		if b.Len() >= 2048 {
			break
		}
	}
	if b.Len() == 0 {
		return "Tool reported an MCP error."
	}
	return strings.ToValidUTF8(b.String(), "")
}
