package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
)

type CodexSessionsInput struct {
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	Archived bool   `json:"archived,omitempty"`
}
type CodexSessionInput struct {
	SessionID        string `json:"session_id"`
	Cursor           string `json:"cursor,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	OutputLimitBytes int    `json:"output_limit_bytes,omitempty"`
}
type CodexTaskInput struct {
	SessionID       string `json:"session_id"`
	Message         string `json:"message"`
	Mode            string `json:"mode,omitempty" jsonschema:"queue (default) for existing session owner, or start a read-only turn in an already loaded idle session."`
	Model           string `json:"model,omitempty" jsonschema:"Optional live Codex model identifier; only supported by read-only start, not owner-managed queue."`
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"Model-supported reasoning effort for read-only start; requires model."`
	IdempotencyKey  string `json:"idempotency_key"`
}
type CodexTaskProbeInput struct {
	SessionID string `json:"session_id"`
	QueueID   string `json:"queue_id,omitempty"`
}

type CodexTaskStatusInput struct {
	SessionID   string `json:"session_id"`
	QueueID     string `json:"queue_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
	QueueCursor string `json:"queue_cursor,omitempty"`
	TurnCursor  string `json:"turn_cursor,omitempty"`
}

// CodexCapacityStatus reports only observable session state. Account-wide quota
// is intentionally omitted: the bridge has no authenticated quota source.
type CodexCapacityStatus struct {
	Source          string  `json:"source"`
	ObservedAt      string  `json:"observed_at"`
	SessionID       string  `json:"session_id"`
	TurnState       string  `json:"turn_state"`
	QuotaRemaining  *int64  `json:"quota_remaining"`
	ResetAt         *string `json:"reset_at"`
	Confidence      string  `json:"confidence"`
	HandoffRequired bool    `json:"handoff_required"`
}

type Deps struct {
	Store *idempotency.Store
	RPC   func(context.Context, string, any) (map[string]any, error)
}

type codexUnknownError struct{ error }

func (codexUnknownError) UnknownOutcome() bool { return true }

func BrokerRPC(ctx context.Context, operation string, input any) (map[string]any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	output, err := brokerclient.Run(child, "codex", operation, string(raw))
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if len(output) > 48000 {
		return nil, errors.New("Codex output budget exceeded")
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func Register(add func(tooldef.Def), d Deps) {
	store, codexRPC := d.Store, d.RPC
	closed := false
	nonDestructive := false
	add(tooldef.New(&mcp.Tool{Name: "codex_sessions_list", Description: "Find local Codex sessions through the existing daemon; bounded pagination. Cloud-only history is not verified. Capability codex.sessions_list.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexSessionsInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.Limit == 0 {
			in.Limit = 10
		}
		if in.Limit < 1 || in.Limit > 30 {
			return nil, nil, errors.New("limit must be 1..30")
		}
		out, err := codexRPC(ctx, "sessions_list", in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "codex_session_read", Description: "Read a bounded page of original local Codex messages and command outcomes, with redaction and UNTRUSTED provenance. Never resumes a session. Capability codex.session_read.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexSessionInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.Limit == 0 {
			in.Limit = 3
		}
		if in.OutputLimitBytes == 0 {
			in.OutputLimitBytes = 16000
		}
		out, err := codexRPC(ctx, "session_read", in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "codex_task_submit", Description: "Send one idempotent user task to an existing loaded Codex session. Default queues it for the owner; start requires idle and forces read-only sandbox. Never creates/resumes another coding worker. Probe with codex_task_status and read results with codex_session_read. Capability codex.task_submit.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexTaskInput) (*mcp.CallToolResult, map[string]any, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" || len(in.Message) > 4096 {
			return nil, nil, errors.New("retry key and message up to 4096 bytes required")
		}
		if in.Mode == "" {
			in.Mode = "queue"
		}
		if in.Mode != "queue" && in.Mode != "start" {
			return nil, nil, errors.New("mode must be queue or start")
		}
		if in.ReasoningEffort != "" && in.Model == "" {
			return nil, nil, errors.New("reasoning_effort requires a model")
		}
		if in.Model != "" && !codexModelIDValid(in.Model) {
			return nil, nil, errors.New("invalid model id")
		}
		switch in.ReasoningEffort {
		case "", "minimal", "low", "medium", "high", "xhigh":
		default:
			return nil, nil, errors.New("unsupported reasoning effort")
		}
		if in.Mode != "start" && (in.Model != "" || in.ReasoningEffort != "") {
			return nil, nil, errors.New("model and reasoning override require an idle read-only turn; owner queue cannot pin per-task settings")
		}
		sum := sha256.Sum256([]byte(in.IdempotencyKey))
		requestID := hex.EncodeToString(sum[:16])
		payload := map[string]any{"session_id": in.SessionID, "message": in.Message, "request_id": requestID, "mode": in.Mode}
		if in.Model != "" {
			payload["model"] = in.Model
		}
		if in.ReasoningEffort != "" {
			payload["reasoning_effort"] = in.ReasoningEffort
		}
		out, err := idempotency.Run(store, in.IdempotencyKey, "codex_task_submit", payload, func() (map[string]any, error) {
			out, err := codexRPC(ctx, "task_submit", payload)
			if err != nil {
				return nil, codexUnknownError{err}
			}
			return out, nil
		})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "codex_capacity_status", Description: "Read-only Codex session capacity evidence. Account quota is unknown unless a verified quota API exists; never infer it from turn usage. No takeover or lease mutation.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexTaskProbeInput) (*mcp.CallToolResult, CodexCapacityStatus, error) {
		if strings.TrimSpace(in.SessionID) == "" {
			return nil, CodexCapacityStatus{}, errors.New("session_id required")
		}
		out, err := codexRPC(ctx, "task_status", in)
		if err != nil {
			return nil, CodexCapacityStatus{}, err
		}
		state := codexCapacityState(out, in.SessionID)
		return nil, CodexCapacityStatus{Source: "codex_task_status", ObservedAt: time.Now().UTC().Format(time.RFC3339), SessionID: in.SessionID, TurnState: state, QuotaRemaining: nil, ResetAt: nil, Confidence: "session-only; account-quota-unknown", HandoffRequired: false}, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "codex_task_status", Description: "Read-only bounded reconciliation by queue ID, request ID or turn ID. Returns queued/running/completed/failed/blocked/unknown from exact submission and linked turn evidence. Queue absence alone is unknown; completion is not workflow DONE or writer authorization. Follow returned queue/turn cursors for older evidence.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexTaskStatusInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := codexRPC(ctx, "task_status", in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "codex_task_cancel", Description: "Compensate a queued submission by its natural session/queue key. Cannot interrupt the owner's active turn or undo already completed work.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexTaskProbeInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := codexRPC(ctx, "task_cancel", in)
		return nil, out, err
	}))
}

// codexModelIDValid accepts only literal model identifiers. The live model
// catalog is verified by the backend before any Codex turn is started.
func codexModelIDValid(model string) bool {
	if len(model) == 0 || len(model) > 80 {
		return false
	}
	for _, ch := range model {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func codexCapacityState(out map[string]any, sessionID string) string {
	if raw, present := out["session"]; present {
		session, ok := raw.(map[string]any)
		if !ok || session["session_id"] != sessionID {
			return "unknown"
		}
		status, ok := session["status"].(map[string]any)
		if !ok {
			return "unknown"
		}
		state, _ := status["type"].(string)
		switch state {
		case "active", "idle", "notLoaded", "systemError":
			return state
		}
		return "unknown"
	}
	if state, ok := out["state"].(string); ok {
		switch state {
		case "running", "queued", "idle", "completed", "failed", "cancelled", "blocked":
			return state
		}
	}
	return "unknown"
}
