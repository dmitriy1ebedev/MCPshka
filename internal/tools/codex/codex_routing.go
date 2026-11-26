package codex

// Pure R3 model preview: client-provided catalog and turn state are UNTRUSTED.
// No Codex process, RPC, idempotency entry, or repository lease is touched.
import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/codexcoord"
	"mcpshka/internal/tooldef"
)

type CodexRoutePreviewInput struct {
	TaskID          string                         `json:"task_id"`
	SessionID       string                         `json:"session_id"`
	Model           string                         `json:"model"`
	ReasoningEffort string                         `json:"reasoning_effort"`
	Catalog         []codexcoord.ModelCapability   `json:"catalog"`
	Turn            *codexcoord.TurnSwitchEvidence `json:"turn,omitempty"`
}

type CodexRoutePreviewOutput struct {
	Trust           string                     `json:"trust"`
	CatalogVerified bool                       `json:"catalog_verified"`
	DispatchEnabled bool                       `json:"dispatch_enabled"`
	Model           codexcoord.ModelChoice     `json:"model"`
	Switch          *codexcoord.TurnSwitchPlan `json:"switch,omitempty"`
}

func RegisterRoute(add func(tooldef.Def)) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "codex_route_preview",
		Description: "Offline model/reasoning and turn-switch preview with caller-supplied UNTRUSTED catalog/evidence. Rejects unsupported exact settings. Never invokes Codex, alters an active turn, changes global model, verifies quota, or authorizes dispatch.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexRoutePreviewInput) (*mcp.CallToolResult, CodexRoutePreviewOutput, error) {
		preference := codexcoord.ModelPreference{
			TaskID: in.TaskID, SessionID: in.SessionID, Model: in.Model, Effort: in.ReasoningEffort,
		}
		model, err := codexcoord.SelectModel(preference, in.Catalog)
		if err != nil {
			return nil, CodexRoutePreviewOutput{}, err
		}
		out := CodexRoutePreviewOutput{
			Trust: "UNTRUSTED", CatalogVerified: false, DispatchEnabled: false, Model: model,
		}
		if in.Turn != nil {
			candidate := codexcoord.PlanTurnSwitch(model, *in.Turn)
			candidate.DispatchEnabled = false
			out.Switch = &candidate
		}
		return nil, out, nil
	}))
}
