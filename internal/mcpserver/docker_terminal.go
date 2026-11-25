package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/dockerterminal"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
)

type DockerTerminalOpenInput struct {
	Container             string `json:"container"`
	IdempotencyKey        string `json:"idempotency_key"`
	ContractApprovalToken string `json:"contract_approval_token,omitempty"`
}
type DockerTerminalStatusInput struct {
	ID        string `json:"id,omitempty"`
	Container string `json:"container,omitempty"`
	Cursor    int64  `json:"cursor,omitempty"`
}
type DockerTerminalIOInput struct {
	ID             string `json:"id"`
	Action         string `json:"action" jsonschema:"write, resize or close. Closing requests termination; use status to confirm fencing."`
	DataBase64     string `json:"data_base64,omitempty" jsonschema:"At most 2048 decoded input bytes. Ctrl+] closes the session. Never put credentials in shell input."`
	Rows           int    `json:"rows,omitempty"`
	Columns        int    `json:"columns,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

func isDockerTerminal(name string) bool { return strings.HasPrefix(name, "docker_terminal_") }

func terminalCall(ctx context.Context, in dockerterminal.TerminalInput) (dockerterminal.TerminalStatus, error) {
	var out dockerterminal.TerminalStatus
	if !brokerclient.Enabled() {
		return out, errors.New("Docker terminal requires protected broker; no local fallback")
	}
	in.Owner = uploadOwner(ctx)
	b, e := json.Marshal(in)
	if e != nil {
		return out, e
	}
	if len(b) > 4096 {
		return out, errors.New("terminal request budget")
	}
	raw, e := brokerclient.Run(ctx, "docker-terminal", string(b))
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out)
	return out, e
}

func registerDockerTerminalTools(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "docker_terminal_status", Description: "Read this authenticated owner's Docker exec/TTY state and bounded output by byte cursor. UNKNOWN does not prove termination. Other owners' sessions are denied.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in DockerTerminalStatusInput) (*mcp.CallToolResult, dockerterminal.TerminalStatus, error) {
		out, e := terminalCall(ctx, dockerterminal.TerminalInput{Action: "status", ID: in.ID, Container: in.Container, Cursor: in.Cursor})
		return nil, out, e
	})
	toolPreflights["docker_terminal_open"] = func(ctx context.Context, in map[string]any) error {
		container, _ := in["container"].(string)
		if _, err := terminalCall(ctx, dockerterminal.TerminalInput{Action: "status", Container: container}); err != nil && strings.Contains(err.Error(), "not allowlisted") {
			return fmt.Errorf("container %q is not allowlisted for terminals, so no approval can open one; for Home Assistant use ha_inventory and ha_config_read", container)
		}
		return nil
	}
	addTool(r, &mcp.Tool{Name: "docker_terminal_open", Description: "Open one approval-gated interactive Docker TTY in an explicitly allowlisted container. Dedicated non-root shell UID, read-only filesystem, bounded resources and lifetime. One writer per immutable container ID; no raw Docker socket or root shell.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in DockerTerminalOpenInput) (*mcp.CallToolResult, dockerterminal.TerminalStatus, error) {
		out, e := terminalCall(ctx, dockerterminal.TerminalInput{Action: "open", Container: in.Container, Key: in.IdempotencyKey})
		return nil, out, e
	})
	addTool(r, &mcp.Tool{Name: "docker_terminal_io", Description: "Write bounded input, resize or close an owned interactive Docker TTY. Idempotent input is never retransmitted after UNKNOWN. Closing does not undo command effects or prove fencing; read terminal status.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in DockerTerminalIOInput) (*mcp.CallToolResult, dockerterminal.TerminalStatus, error) {
		out, e := terminalCall(ctx, dockerterminal.TerminalInput{Action: in.Action, ID: in.ID, Key: in.IdempotencyKey, Data: in.DataBase64, Rows: in.Rows, Columns: in.Columns})
		out.Data = ""
		return nil, out, e
	})
}
