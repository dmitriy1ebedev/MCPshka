package approvedtask

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/adminops"
)

type CallResultInput struct {
	CallID string `json:"call_id" jsonschema:"Opaque 32-hex approved_task_prepare ID."`
}

type CallResult struct {
	CallID        string `json:"call_id"`
	State         string `json:"state"`
	ApprovalState string `json:"approval_state,omitempty"`
	Effect        string `json:"effect,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
}

var approvalCallID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type statusReader func(context.Context, string, map[string]any) (map[string]any, error)

func RegisterResult(add func(tooldef.Def), read statusReader) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "get_tool_result", Description: "Read the saved status of an approved task by its original call_id (the id from approved_task_prepare). Returns a bounded receipt without command arguments or output. Never starts, approves or replays a task. For a job_id use job_wait.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CallResultInput) (*mcp.CallToolResult, CallResult, error) {
			if _, err := adminops.Owner(ctx); err != nil {
				return nil, CallResult{}, err
			}
			out, err := readCallResult(ctx, in.CallID, read)
			return nil, out, err
		}))
}

func readCallResult(ctx context.Context, id string, read statusReader) (CallResult, error) {
	if !approvalCallID.MatchString(id) {
		return CallResult{}, errors.New("call_id must be the original task ID")
	}
	out, err := read(ctx, "approved_task_status", map[string]any{"id": id})
	if err != nil {
		return CallResult{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return CallResult{}, err
	}
	var result struct {
		ID            string `json:"id"`
		State         string `json:"state"`
		ApprovalState string `json:"approval_state"`
		Effect        string `json:"effect"`
		ExitCode      *int   `json:"exit_code"`
	}
	if len(raw) > 256<<10 || json.Unmarshal(raw, &result) != nil || result.ID != id {
		return CallResult{}, errors.New("original task receipt is unavailable")
	}
	return CallResult{CallID: id, State: result.State, ApprovalState: result.ApprovalState,
		Effect: result.Effect, ExitCode: result.ExitCode}, nil
}
