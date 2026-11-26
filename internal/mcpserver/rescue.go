package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
)

type RescueStatusInput struct {
	RequestID string `json:"request_id,omitempty"`
}
type RescueStartInput struct {
	Mode           string `json:"mode" jsonschema:"reconnect, restart (no active jobs), or guarded rollback to retained previous candidate."`
	IdempotencyKey string `json:"idempotency_key"`
}

var rescueCall = func(ctx context.Context, args ...string) (map[string]any, error) {
	raw, err := brokerclient.Run(ctx, "rescue", args...)
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, errors.New("rescue output budget exceeded")
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}

// rescueUnavailable reports only the locally observed absence of the fixed
// helper. A missing helper is not a successful rescue or an OOB control path.
// All other errors and existing helpers continue through the trusted broker.
func rescueUnavailable(path string) (map[string]any, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{
			"state":                    "unconfigured",
			"helper_installed":         false,
			"recovery_ready":           false,
			"verified_independent_oob": 0,
			"reason":                   "root-owned independent rescue helper not provisioned; operator action required",
		}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("rescue helper path is not a regular file: HOLD")
	}
	return nil, false, nil
}

func registerRescueTools(r *toolRegistry) {
	closed := false
	addTool(r, &mcp.Tool{Name: "rescue_status", Description: "Bounded fixed-unit health or durable recovery receipt. No overlay transport is mislabeled as independent OOB management. Status never replays a mutation.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in RescueStatusInput) (*mcp.CallToolResult, map[string]any, error) {
		args := []string{"status"}
		if in.RequestID != "" {
			if len(in.RequestID) != 32 {
				return nil, nil, errors.New("invalid recovery identity")
			}
			if _, err := hex.DecodeString(in.RequestID); err != nil {
				return nil, nil, err
			}
			args = append(args, in.RequestID)
		}
		if in.RequestID == "" {
			status, unconfigured, err := rescueUnavailable("/usr/local/libexec/mcpshka-rescue.py")
			if err != nil {
				return nil, nil, err
			}
			if unconfigured {
				return nil, status, nil
			}
		}
		out, err := rescueCall(ctx, args...)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "rescue_start", Description: "Schedule one independent bounded fixed-unit recovery. reconnect restores the tunnel; restart rejects active jobs; rollback delegates to the guarded updater and retained previous candidate. Deployment lock prevents interference. Lost response is UNKNOWN: probe receipt, never blindly retry. No routes, DNS, VPN or arbitrary services are changed.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in RescueStartInput) (*mcp.CallToolResult, map[string]any, error) {
		switch in.Mode {
		case "reconnect", "restart", "rollback":
		default:
			return nil, nil, errors.New("unsupported recovery mode")
		}
		sum := sha256.Sum256([]byte(in.IdempotencyKey))
		id := hex.EncodeToString(sum[:16])
		out, err := rescueCall(ctx, "start", in.Mode, id)
		if err != nil {
			return nil, nil, codexUnknownError{err}
		}
		return nil, out, nil
	})
}
