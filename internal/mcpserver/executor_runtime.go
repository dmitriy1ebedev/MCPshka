package mcpserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/executor"
	"mcpshka/internal/jobs"
	"time"
)

type ExecutorRuntimeInput struct {
	PermitID string `json:"permit_id"`
	LaunchID string `json:"launch_id"`
	Workdir  string `json:"workdir"`
}

func executorRuntimeStatus(ctx context.Context, m *jobs.Manager, in ExecutorRuntimeInput) (executor.Snapshot, error) {
	if !executorCanaryID.MatchString(in.PermitID) || !executorCanaryID.MatchString(in.LaunchID) {
		return executor.Snapshot{}, errors.New("executor runtime requires fixed permit and launch identities")
	}
	if _, err := m.Policy().CheckWorkdir(in.Workdir); err != nil {
		return executor.Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := brokerclient.Run(ctx, "executor", "status", in.PermitID, in.LaunchID)
	if err != nil {
		return executor.Snapshot{}, err
	}
	var s executor.Snapshot
	if len(raw) > 65536 || decodeIntegrationInput(string(raw), &s) != nil || s.Pin.PermitID != in.PermitID || s.Pin.Generation == 0 || !executorCanaryID.MatchString(s.Pin.LeaseID) || !executorCanaryID.MatchString(s.Pin.ExecutionID) || s.Revision == 0 || len(s.Launches) > 1 || (s.State != "ACTIVE" && s.State != "REVOKING" && s.State != "FENCED") {
		return executor.Snapshot{}, errors.New("executor runtime evidence pins differ: HOLD")
	}
	for _, l := range s.Launches {
		if l.ID != in.LaunchID {
			return executor.Snapshot{}, errors.New("executor runtime launch differs: HOLD")
		}
	}
	if s.Source != nil {
		if _, err := m.Policy().CheckWorkdir(s.Source.Repo); err != nil {
			return executor.Snapshot{}, err
		}
	}
	return s, nil
}
func registerExecutorRuntimeStatus(r *toolRegistry, m *jobs.Manager) {
	addTool(r, &mcp.Tool{Name: "executor_runtime_status", Description: "Read root-owned fixed permit/launch state and durable exit observation. FENCED without result is not successful completion; ACTIVE or missing exit stays unresolved.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in ExecutorRuntimeInput) (*mcp.CallToolResult, executor.Snapshot, error) {
		out, err := executorRuntimeStatus(ctx, m, in)
		return nil, out, err
	})
}
