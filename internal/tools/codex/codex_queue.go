package codex

// R3 read-only queue visibility. Catalog verification, task admission and
// Codex dispatch remain separate, approval-gated operations.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/codexcoord"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tooldef"
)

type CodexQueueIDInput struct {
	ID string `json:"id"`
}

type CodexQueuePageInput struct {
	Limit int `json:"limit,omitempty"`
}

type CodexQueuePlanInput struct {
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	Worktree        string `json:"worktree"`
	RoadmapSHA      string `json:"roadmap_sha256"`
	Message         string `json:"message"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type CodexQueueSummary struct {
	ID              string           `json:"id"`
	SessionID       string           `json:"session_id"`
	Worktree        string           `json:"worktree"`
	RoadmapSHA      string           `json:"roadmap_sha256"`
	MessageSHA      string           `json:"message_sha256"`
	Model           string           `json:"model"`
	ReasoningEffort string           `json:"reasoning_effort"`
	State           codexcoord.State `json:"state"`
	Revision        uint64           `json:"revision"`
	DispatchID      string           `json:"dispatch_id,omitempty"`
}

type CodexQueueReadResult struct {
	Source          string            `json:"source"`
	Trust           string            `json:"trust"`
	DispatchEnabled bool              `json:"dispatch_enabled"`
	Task            CodexQueueSummary `json:"task"`
	NextSafeAction  string            `json:"next_safe_action"`
}

type CodexQueuePageResult struct {
	Source          string              `json:"source"`
	DispatchEnabled bool                `json:"dispatch_enabled"`
	Tasks           []CodexQueueSummary `json:"tasks"`
}

func queueSummary(t codexcoord.Task) CodexQueueSummary {
	digest := sha256.Sum256([]byte(t.Message))
	return CodexQueueSummary{
		ID: t.ID, SessionID: t.SessionID, Worktree: t.Worktree,
		RoadmapSHA: t.RoadmapSHA, MessageSHA: hex.EncodeToString(digest[:]),
		Model: t.Model, ReasoningEffort: t.Effort, State: t.State,
		Revision: t.Revision, DispatchID: t.DispatchID,
	}
}

func queueFromDB(db *statestore.DB) (*codexcoord.Queue, error) {
	if db == nil {
		return nil, errors.New("durable SQLite queue unavailable: HOLD")
	}
	return codexcoord.New(db)
}

func RegisterQueue(add func(tooldef.Def), db *statestore.DB) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "codex_queue_plan_preview",
		Description: "Validate only the shape of a pinned Codex task (model, reasoning, session, worktree, ROADMAP); no Codex RPC, enqueue, ownership grant, model verification or worker launch. Message is returned only as a digest.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexQueuePlanInput) (*mcp.CallToolResult, CodexQueueReadResult, error) {
		t := codexcoord.Task{ID: in.ID, Provider: "codex", SessionID: in.SessionID,
			Worktree: in.Worktree, RoadmapSHA: in.RoadmapSHA, Message: in.Message,
			Model: in.Model, Effort: in.ReasoningEffort}
		if err := codexcoord.ValidateShape(t); err != nil {
			return nil, CodexQueueReadResult{}, err
		}
		return nil, CodexQueueReadResult{
			Source: "syntactic_preview", Trust: "UNTRUSTED", DispatchEnabled: false,
			Task: queueSummary(t), NextSafeAction: "live_model_catalog_and_writer_authorization_required",
		}, nil
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "codex_queue_status",
		Description: "Read a durable R3 task by ID, without task message content or worker effects. A reservation is not permission to dispatch.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexQueueIDInput) (*mcp.CallToolResult, CodexQueueReadResult, error) {
		q, err := queueFromDB(db)
		if err != nil {
			return nil, CodexQueueReadResult{}, err
		}
		receipt, err := q.InspectReservation(in.ID)
		if err != nil {
			return nil, CodexQueueReadResult{}, err
		}
		t := receipt.Task
		next := receipt.NextSafeAction
		if next == "read_only_reconcile" {
			next = "catalog_and_owner_checks_required"
		}
		return nil, CodexQueueReadResult{
			Source: "sqlite", Trust: "UNTRUSTED", DispatchEnabled: false,
			Task: queueSummary(t), NextSafeAction: next,
		}, nil
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "codex_queue_pending",
		Description: "Read a bounded list of pending, not-yet-dispatched R3 tasks. Excludes messages and any implicit execution permission.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexQueuePageInput) (*mcp.CallToolResult, CodexQueuePageResult, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 10
		}
		if limit < 1 || limit > 20 {
			return nil, CodexQueuePageResult{}, errors.New("pending limit must be 1..20")
		}
		q, err := queueFromDB(db)
		if err != nil {
			return nil, CodexQueuePageResult{}, err
		}
		tasks, err := q.Pending(limit)
		if err != nil {
			return nil, CodexQueuePageResult{}, err
		}
		rows := make([]CodexQueueSummary, 0, len(tasks))
		for _, t := range tasks {
			rows = append(rows, queueSummary(t))
		}
		return nil, CodexQueuePageResult{Source: "sqlite", DispatchEnabled: false, Tasks: rows}, nil
	}))
}
