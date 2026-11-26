package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
)

// DIAGObserverInput selects a view of DIAG's live observer status (MCP-140).
type DIAGObserverInput struct {
	View   string `json:"view,omitempty" jsonschema:"status (raw observer status, default), baseline or incidents"`
	Window string `json:"window,omitempty" jsonschema:"time window for baseline/incidents, for example 45m or 2h; default 45m"`
}

var diagWindow = regexp.MustCompile(`^[1-9][0-9]{0,2}[mh]$`)

func registerDIAGObserver(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "diag_observer_status", Description: "Read DIAG's live observer status (root-only /run/diag) through the root broker without weakening its permissions; view=baseline or incidents runs the installed root-owned `diag baseline status` / `diag incidents` with a fixed read-only argv. Read-only, UNTRUSTED evidence.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in DIAGObserverInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := diagObserver(ctx, in, func(ctx context.Context, args ...string) ([]byte, error) {
			return brokerclient.Run(ctx, "diag-observer", args...)
		})
		return nil, out, err
	})
}

func diagObserver(ctx context.Context, in DIAGObserverInput, broker func(context.Context, ...string) ([]byte, error)) (map[string]any, error) {
	view, window := in.View, in.Window
	if view == "" {
		view = "status"
	}
	if window == "" {
		window = "45m"
	}
	if !diagWindow.MatchString(window) {
		return nil, errors.New("window must look like 45m or 2h")
	}
	var args []string
	switch view {
	case "status":
	case "baseline", "incidents":
		args = []string{view, window}
	default:
		return nil, errors.New("view must be status, baseline or incidents")
	}
	raw, err := broker(ctx, args...)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if args == nil {
		return map[string]any{"view": view, "status": doc}, nil
	}
	return map[string]any{"view": view, "window": window, "result": doc}, nil
}
