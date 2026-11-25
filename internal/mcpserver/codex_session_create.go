package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/rootbroker"
)

type CodexSessionCreateInput struct {
	AdminSessionID string `json:"admin_session_id"`
	CreationKey    string `json:"creation_key" jsonschema:"Stable 32 lowercase hex key for one new session. Reuse it after disconnect; never replace an UNKNOWN key."`
	LoadKey        string `json:"load_key,omitempty" jsonschema:"Separate 32 lowercase hex key for loading the already identified session."`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func codexSessionTool(action string) (string, bool) {
	switch action {
	case "create":
		return "codex_session_create", false
	case "load":
		return "codex_session_load", false
	case "status":
		return "codex_session_creation_status", true
	default:
		return "", false
	}
}

func callAdminCodexSession(ctx context.Context, action string, in CodexSessionCreateInput) (rootbroker.CodexSessionResult, error) {
	owner, err := adminOwner(ctx)
	if err != nil {
		return rootbroker.CodexSessionResult{}, err
	}
	return runAdminSession[rootbroker.CodexSessionResult](ctx, "codex-"+action, in.AdminSessionID, owner, in.CreationKey, in.LoadKey)
}

func registerCodexSessionCreateTools(r *toolRegistry) {
	closed := false
	for _, action := range []string{"create", "load", "status"} {
		name, readOnly := codexSessionTool(action)
		description := "Inspect durable owner-bound Codex creation/load evidence. READY is timestamped last observation; use codex_session_read/codex_task_status for current runtime state. UNKNOWN never authorizes creating a replacement session."
		if action == "create" {
			description = "Create one new read-only Codex session for MCPshka under the current Face ID grant, through an independent bounded worker. No prompt or model turn is started. Root intent and returned thread ID survive frontend disconnect/restart. Use the same creation_key after a lost response. READY requires read-only policy and disabled MCP tools; no writer authority is granted."
		}
		if action == "load" {
			description = "Load/resume only the exact Codex thread identified by this owner's durable creation intent. Requires a separate load_key and active administrative grant. Forces read-only policy and isolated tools, serializes with native task admission, and never retries thread creation or selects an arbitrary existing thread."
		}
		addTool(r, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in CodexSessionCreateInput) (*mcp.CallToolResult, rootbroker.CodexSessionResult, error) {
			if !readOnly && in.IdempotencyKey == "" {
				return nil, rootbroker.CodexSessionResult{}, errors.New("Codex session mutation retry key required")
			}
			out, err := callAdminCodexSession(ctx, action, in)
			return nil, out, err
		})
	}
}

type codexSessionCompatInput struct {
	Action         string `json:"action"`
	AdminSessionID string `json:"admin_session_id"`
	CreationKey    string `json:"creation_key"`
	LoadKey        string `json:"load_key,omitempty"`
}

func isCodexSessionCompat(name string, in map[string]any) bool {
	command, _ := in["command"].(string)
	args, _ := in["args"].([]any)
	return name == "exec_start" && (command == "mcpshka" || command == "/usr/local/bin/mcpshka") && len(args) > 0 && args[0] == "codex-session"
}

func codexSessionCompatRequest(in ExecStartInput) (codexSessionCompatInput, error) {
	var request codexSessionCompatInput
	if len(in.Args) != 2 || len(in.Args[1]) > 2048 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || in.IdempotencyKey == "" {
		return request, errors.New("typed Codex session request without overrides required")
	}
	d := json.NewDecoder(strings.NewReader(in.Args[1]))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF {
		return request, errors.New("invalid Codex session JSON")
	}
	if name, _ := codexSessionTool(request.Action); name == "" {
		return request, errors.New("unsupported Codex session action")
	}
	return request, nil
}

func codexSessionCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	request, err := codexSessionCompatRequest(in)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err = adminOwner(ctx); err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err = m.Policy().CheckWorkdir(in.Workdir); err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err = m.Policy().DecideCommand("mcpshka-codex-session", nil); err != nil {
		return jobs.Snapshot{}, err
	}
	out, err := callAdminCodexSession(ctx, request.Action, CodexSessionCreateInput{AdminSessionID: request.AdminSessionID, CreationKey: request.CreationKey, LoadKey: request.LoadKey, IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return jobs.Snapshot{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt("mcpshka", []string{"codex-session", request.Action, request.CreationKey, request.LoadKey}, in.Workdir, string(raw))
}
