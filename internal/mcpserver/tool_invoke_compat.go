package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mcpshka/internal/jobs"
)

// A client keeps the tools/list snapshot it saw when the connector was added
// (ChatGPT does), so tools added later — tool_invoke, agent_task_status,
// mcpshka_guide — never appear for it. exec_start is in every snapshot, so
// command=mcpshka with these first arguments reaches any tool:
//
//	tool-invoke <name> [<arguments JSON object>]
//	guide [<topic>]
//	resolve <goal>
//
// The call goes through r.invoke, i.e. the target's full wrapper (scope,
// Face ID, admission gate, retry key, receipts); the result is stored as a
// finished job whose output job_output returns.
const toolInvokeCompatHint = "client tools/list is stale: refresh the connector, or call exec_start command=mcpshka args=[\"tool-invoke\",\"<tool>\",\"<arguments JSON>\"] (also [\"guide\",\"rules\"] and [\"resolve\",\"<goal>\"]) and read the result with job_output"

// execStartRefusalHint follows every exec_start refusal.
const execStartRefusalHint = "a typed MCPshka tool may fit: exec_start command=mcpshka args=[\"resolve\",\"<goal>\"] finds it and args=[\"tool-invoke\",\"<tool>\",\"<arguments JSON>\"] calls it even when it is missing from your tools/list"

func isToolInvokeCompat(name string, input map[string]any) bool {
	if name != "exec_start" {
		return false
	}
	command, _ := input["command"].(string)
	if command != "mcpshka" && command != "/usr/local/bin/mcpshka" {
		return false
	}
	args, _ := input["args"].([]any)
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "tool-invoke", "guide", "resolve":
		return true
	}
	return false
}

// toolInvokeCompatRequest maps exec_start arguments to one tool call. The
// target's own idempotency_key travels inside the arguments JSON; exec_start's
// key is copied in when the target needs one and the caller left it out.
func toolInvokeCompatRequest(in ExecStartInput) (ToolInvokeInput, error) {
	if len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || in.LeaseToken != "" {
		return ToolInvokeInput{}, errors.New("tool-invoke takes no env, approval, plan or lease overrides; put them in the arguments JSON")
	}
	switch in.Args[0] {
	case "guide":
		if len(in.Args) > 2 {
			return ToolInvokeInput{}, errors.New("guide takes at most one topic")
		}
		args := map[string]any{}
		if len(in.Args) == 2 {
			args["topic"] = in.Args[1]
		}
		return ToolInvokeInput{Name: "mcpshka_guide", Arguments: args}, nil
	case "resolve":
		if len(in.Args) != 2 {
			return ToolInvokeInput{}, errors.New("resolve takes one goal")
		}
		return ToolInvokeInput{Name: "tool_resolve", Arguments: map[string]any{"goal": in.Args[1], "compact": true}}, nil
	}
	if len(in.Args) < 2 || len(in.Args) > 3 || strings.TrimSpace(in.Args[1]) == "" {
		return ToolInvokeInput{}, errors.New("tool-invoke requires a tool name and an optional arguments JSON object")
	}
	args := map[string]any{}
	if len(in.Args) == 3 && strings.TrimSpace(in.Args[2]) != "" {
		if err := json.Unmarshal([]byte(in.Args[2]), &args); err != nil {
			return ToolInvokeInput{}, fmt.Errorf("tool-invoke arguments must be one JSON object: %w", err)
		}
		if args == nil {
			args = map[string]any{}
		}
	}
	return ToolInvokeInput{Name: in.Args[1], Arguments: args}, nil
}

func (r *toolRegistry) toolInvokeCompat(ctx context.Context, input map[string]any) (jobs.Snapshot, error) {
	if r.receipts == nil {
		return jobs.Snapshot{}, errors.New("tool-invoke compatibility is unavailable")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	var command ExecStartInput
	if err := json.Unmarshal(raw, &command); err != nil {
		return jobs.Snapshot{}, errors.New("invalid tool-invoke compatibility request")
	}
	request, err := toolInvokeCompatRequest(command)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if _, ok := request.Arguments["idempotency_key"]; !ok && command.IdempotencyKey != "" && r.needsRetryKey(request.Name) {
		request.Arguments["idempotency_key"] = command.IdempotencyKey
	}
	out, err := r.invoke(withRoute(ctx, "exec_compat"), request)
	if err == nil && out.IsError {
		message := "target returned an error; reconcile the original operation"
		if len(out.Content) > 0 {
			message = strings.ToValidUTF8(out.Content[0][:min(len(out.Content[0]), 2048)], "")
		}
		return jobs.Snapshot{}, fmt.Errorf("%s reported an MCP error: %s", request.Name, message)
	}
	if err != nil {
		return jobs.Snapshot{}, err
	}
	text, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if len(text) > jobs.MaxReadLimit {
		return jobs.Snapshot{}, fmt.Errorf("%s result is %d bytes, over the %d-byte receipt cap; narrow the request", request.Name, len(text), jobs.MaxReadLimit)
	}
	return r.receipts.StoreReceipt("mcpshka", command.Args[:min(len(command.Args), 2)], command.Workdir, string(text))
}

// needsRetryKey reports whether the target's input schema has idempotency_key.
func (r *toolRegistry) needsRetryKey(name string) bool {
	t := r.lookupTool(name).tool
	if t == nil || t.InputSchema == nil {
		return false
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return false
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(raw, &schema) != nil {
		return false
	}
	_, ok := schema.Properties["idempotency_key"]
	return ok
}
