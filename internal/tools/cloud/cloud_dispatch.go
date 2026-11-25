package cloud

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/cloudtasks"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tooldef"
)

type CloudDispatchInput struct {
	SessionID      string `json:"admin_session_id"`
	Provider       string `json:"provider" jsonschema:"codex_cloud or claude_cloud; unsupported routes return a durable blocked receipt."`
	Commit         string `json:"commit" jsonschema:"Full immutable MCPshka source commit; working tree changes are excluded."`
	Prompt         string `json:"prompt"`
	Acceptance     string `json:"acceptance"`
	IdempotencyKey string `json:"idempotency_key"`
}
type CloudPollInput struct {
	SessionID      string `json:"admin_session_id"`
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type cloudDispatchBroker func(context.Context, string, string, string, string) (cloudtasks.DispatchReceipt, error)

func Dispatch(ctx context.Context, j *cloudtasks.Journal, in CloudDispatchInput, owner string, call cloudDispatchBroker) (cloudtasks.DispatchReceipt, error) {
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 {
		return cloudtasks.DispatchReceipt{}, errors.New("bounded idempotency_key required")
	}
	p := cloudtasks.DispatchRequest{ID: cloudtasks.DispatchID(owner, in.IdempotencyKey), Owner: owner, Provider: in.Provider, Commit: in.Commit, Prompt: in.Prompt, Acceptance: in.Acceptance}
	old, won, err := j.Claim(p)
	if err != nil || !won {
		return old, err
	}
	raw, _ := json.Marshal(p)
	next, err := call(ctx, "cloud-agent-start", in.SessionID, owner, string(raw))
	if err != nil {
		return old, nil
	} // UNKNOWN must be reconciled, never resubmitted.
	return j.Finish(old, next)
}

type Deps struct {
	DB             *statestore.DB
	Owner          func(context.Context) (string, error)
	RequireSession func(context.Context, string, string) error
	Call           cloudDispatchBroker
}

func RegisterDispatch(add func(tooldef.Def), d Deps) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "cloud_agent_task", Description: "Start one cloud coding task on an immutable MCPshka snapshot under Face ID. Durable SQLite receipt; identical retries never resubmit UNKNOWN. No local agent, apply, main write or deployment. Poll cloud_agent_poll; publish/review the resulting PR, then GitVerse CI and integration_ship.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CloudDispatchInput) (*mcp.CallToolResult, cloudtasks.DispatchReceipt, error) {
			owner, err := d.Owner(ctx)
			if err != nil {
				return nil, cloudtasks.DispatchReceipt{}, err
			}
			if err = d.RequireSession(ctx, in.SessionID, owner); err != nil {
				return nil, cloudtasks.DispatchReceipt{}, err
			}
			j, err := cloudtasks.New(d.DB)
			if err != nil {
				return nil, cloudtasks.DispatchReceipt{}, err
			}
			out, err := Dispatch(ctx, j, in, owner, d.Call)
			return nil, out, err
		}))
	add(tooldef.New(&mcp.Tool{Name: "cloud_agent_poll", Description: "Query the authenticated provider once and update its durable SQLite receipt under Face ID. No task submission or local apply. A completed task without a same-repository result branch/PR remains awaiting_pr; even a result requires GitVerse CI and integration_ship.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CloudPollInput) (*mcp.CallToolResult, cloudtasks.DispatchReceipt, error) {
			owner, err := d.Owner(ctx)
			if err != nil {
				return nil, cloudtasks.DispatchReceipt{}, err
			}
			if err = d.RequireSession(ctx, in.SessionID, owner); err != nil {
				return nil, cloudtasks.DispatchReceipt{}, err
			}
			j, err := cloudtasks.New(d.DB)
			if err != nil {
				return nil, cloudtasks.DispatchReceipt{}, err
			}
			old, err := j.Dispatch(in.ID)
			if err != nil {
				return nil, old, err
			}
			if old.Owner != owner {
				return nil, cloudtasks.DispatchReceipt{}, errors.New("cloud task belongs to another owner")
			}
			if old.ProviderTaskID == "" || old.State == "result" || old.State == "failed" {
				return nil, old, nil
			}
			raw, _ := json.Marshal(old)
			next, err := d.Call(ctx, "cloud-agent-poll", in.SessionID, owner, string(raw))
			if err != nil {
				return nil, old, err
			}
			out, err := j.Finish(old, next)
			return nil, out, err
		}))
}
