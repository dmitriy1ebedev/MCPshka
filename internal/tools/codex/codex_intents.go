package codex

import (
	"context"
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/codexcoord"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tooldef"
)

type CodexIntentCreateInput struct {
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	Worktree        string `json:"worktree"`
	RoadmapSHA      string `json:"roadmap_sha256"`
	Message         string `json:"message"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type CodexIntentUpdateInput struct {
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Model            string `json:"model"`
	ReasoningEffort  string `json:"reasoning_effort"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type CodexIntentCancelInput struct {
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type CodexIntentIDInput struct {
	ID string `json:"id"`
}
type CodexIntentListInput struct {
	Limit int `json:"limit,omitempty"`
}

func RegisterIntents(add func(tooldef.Def), database *statestore.DB) {
	closed, nonDestructive := false, false
	getQueue := func() (*codexcoord.Queue, error) { return codexcoord.New(database) }
	add(tooldef.New(&mcp.Tool{Name: "codex_intent_status", Description: "Inspect a durable non-executable Codex task intent. No Codex RPC, no task text or authority transfer. Unverified model and dispatch=false remain explicit.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CodexIntentIDInput) (*mcp.CallToolResult, map[string]any, error) {
			q, e := getQueue()
			if e != nil {
				return nil, nil, e
			}
			intent, e := q.ReadIntent(in.ID)
			if errors.Is(e, os.ErrNotExist) {
				return nil, map[string]any{"id": in.ID, "found": false, "dispatch_enabled": false}, nil
			}
			if e != nil {
				return nil, nil, e
			}
			return nil, map[string]any{"id": in.ID, "found": true, "intent": codexcoord.Summarize(intent), "dispatch_enabled": false}, nil
		}))
	add(tooldef.New(&mcp.Tool{Name: "codex_intent_list", Description: "List up to 50 durable planning intents. Does not call Codex, reveal task text, or start any turn.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CodexIntentListInput) (*mcp.CallToolResult, map[string]any, error) {
			q, e := getQueue()
			if e != nil {
				return nil, nil, e
			}
			if in.Limit == 0 {
				in.Limit = 20
			}
			plans, e := q.ListIntents(in.Limit)
			return nil, map[string]any{"intents": plans, "dispatch_enabled": false}, e
		}))
	add(tooldef.New(&mcp.Tool{Name: "codex_intent_create", Description: "Persist a non-executable Codex task plan with per-task model/reasoning preference and pinned scope. Catalog verification and dispatch remain disabled; repeat requires the same retry key.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CodexIntentCreateInput) (*mcp.CallToolResult, codexcoord.IntentSummary, error) {
			q, e := getQueue()
			if e != nil {
				return nil, codexcoord.IntentSummary{}, e
			}
			plan := codexcoord.Intent{ID: in.ID, SessionID: in.SessionID, Worktree: in.Worktree, RoadmapSHA: in.RoadmapSHA, Message: in.Message, Model: in.Model, Effort: in.ReasoningEffort}
			summary, e := q.StageIntent(plan)
			return nil, summary, e
		}))
	add(tooldef.New(&mcp.Tool{Name: "codex_intent_update", Description: "Change model/reasoning of a queued non-executable intent with revision-CAS; never alters a live Codex turn.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CodexIntentUpdateInput) (*mcp.CallToolResult, codexcoord.IntentSummary, error) {
			q, e := getQueue()
			if e != nil {
				return nil, codexcoord.IntentSummary{}, e
			}
			result, e := q.ChangeIntent(in.ID, in.ExpectedRevision, in.Model, in.ReasoningEffort)
			return nil, result, e
		}))
	add(tooldef.New(&mcp.Tool{Name: "codex_intent_cancel", Description: "Cancel a non-executable draft by pinned revision. Does not interrupt Codex or delete a worktree.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}},
		func(ctx context.Context, req *mcp.CallToolRequest, in CodexIntentCancelInput) (*mcp.CallToolResult, codexcoord.IntentSummary, error) {
			q, e := getQueue()
			if e != nil {
				return nil, codexcoord.IntentSummary{}, e
			}
			result, e := q.CancelIntent(in.ID, in.ExpectedRevision)
			return nil, result, e
		}))
}
