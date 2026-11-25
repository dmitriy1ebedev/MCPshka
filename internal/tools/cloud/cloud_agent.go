package cloud

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tooldef"
)

const cloudAgentObservationSchema = "mcpshka.cloud-agent-observation/v1"

type CloudAgentTask struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"` // Codex Cloud CLI
	State  string `json:"state,omitempty"`  // GitHub Copilot agent-tasks API
}
type CloudAgentInput struct {
	Provider string           `json:"provider"`
	TaskID   string           `json:"task_id"`
	Tasks    []CloudAgentTask `json:"tasks"`
}
type CloudAgentOutput struct {
	Schema           string `json:"schema"`
	Provider         string `json:"provider"`
	TaskID           string `json:"task_id"`
	State            string `json:"state"`
	ProviderState    string `json:"provider_state"`
	Trust            string `json:"trust"`
	SourceVerified   bool   `json:"source_verified"`
	ArtifactVerified bool   `json:"artifact_verified"`
	DeployAuthorized bool   `json:"deploy_authorized"`
	Next             string `json:"next"`
}

func ObserveAgent(in CloudAgentInput) (CloudAgentOutput, error) {
	if (in.Provider != "codex_cloud" && in.Provider != "copilot_cloud") || !cloudIDRE.MatchString(in.TaskID) {
		return CloudAgentOutput{}, errors.New("invalid cloud agent provider or task ID")
	}
	if len(in.Tasks) > 20 {
		return CloudAgentOutput{}, errors.New("agent task page exceeds limit")
	}
	found := false
	state := "not_observed"
	seen := make(map[string]bool, len(in.Tasks))
	for _, task := range in.Tasks {
		if !cloudIDRE.MatchString(task.ID) || seen[task.ID] {
			return CloudAgentOutput{}, errors.New("invalid or duplicated cloud agent task")
		}
		seen[task.ID] = true
		if task.ID != in.TaskID {
			continue
		}
		found = true
		raw := task.Status
		if in.Provider == "copilot_cloud" {
			raw = task.State
		}
		if len(raw) > 50 {
			raw = ""
		}
		switch strings.ToLower(raw) {
		case "completed", "failed", "cancelled", "timed_out", "queued",
			"in_progress", "running", "pending", "idle", "waiting_for_user":
			state = strings.ToLower(raw)
		default:
			state = "unknown"
		}
	}
	out := CloudAgentOutput{
		Schema: cloudAgentObservationSchema, Provider: in.Provider, TaskID: in.TaskID,
		State: "hold", ProviderState: state, Trust: "UNTRUSTED",
		SourceVerified: false, ArtifactVerified: false, DeployAuthorized: false,
		Next: "authenticate_provider_and_pin_source_run_artifact_before_any_release_promotion",
	}
	if found {
		switch state {
		case "completed":
			out.State = "observed_completed"
		case "failed", "cancelled", "timed_out":
			out.State = "observed_failed"
		}
	}
	return out, nil
}

func RegisterAgent(add func(tooldef.Def)) {
	add(tooldef.New(&mcp.Tool{
		Name:        "cloud_agent_observe",
		Description: "Read-only evaluation of caller-supplied UNTRUSTED Codex Cloud or GitHub Copilot task metadata. Does not query provider API, authenticate claims, dispatch agents, or approve release/deploy.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CloudAgentInput) (*mcp.CallToolResult, CloudAgentOutput, error) {
		result, err := ObserveAgent(in)
		return nil, result, err
	}))
}
