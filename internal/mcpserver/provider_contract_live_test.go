package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/agents"
	"mcpshka/internal/authn"
	"mcpshka/internal/phasegate"
	"mcpshka/internal/providers"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderContractsThroughTools(t *testing.T) {
	oldRead, oldSites, oldHealth := agentTaskRead, providersSnapshot, agentProviderHealth
	t.Cleanup(func() { agentTaskRead, providersSnapshot, agentProviderHealth = oldRead, oldSites, oldHealth })
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	task := agents.Task{ID: "0123456789abcdef", Agent: agents.Codex, State: "quota", FinishedAt: now, ResetAt: reset}
	agentTaskRead = func(string) (agents.Task, error) { return task, nil }
	providersSnapshot = func(context.Context) []providers.Status { return nil }
	agentProviderHealth = func(context.Context, time.Time) []agents.ProviderHealth {
		return []agents.ProviderHealth{{Name: agents.Codex, Identity: agents.WithProvider(task, now).Identity}}
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerAgentTools(r)
	registerProvidersTool(r)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "test", Role: "reader", Scopes: []string{"agent_task_status", "providers_status"}})
	v, err := r.readers["agent_task_status"](ctx, map[string]any{"id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := v.(agents.Task)
	if got.State != "quota" || got.Quota.State != "EXHAUSTED" || got.Quota.ResetAt == nil || !got.Quota.ResetAt.Equal(reset) || got.Identity.ID != agents.Codex {
		t.Fatal(got)
	}
	p, err := r.readers["providers_status"](ctx, map[string]any{})
	if err != nil || p.(ProvidersStatusResult).Agents[0].Identity.Quota.State != "EXHAUSTED" {
		t.Fatal(p, err)
	}
	task.State = "done"
	v, err = r.readers["agent_task_status"](ctx, map[string]any{"id": task.ID})
	if err != nil || v.(agents.Task).Quota.State != "UNKNOWN" || v.(agents.Task).State != "done_unverified" || v.(agents.Task).NextAction == "" {
		t.Fatal(v, err)
	}
}

func TestPhaseGateRejectsAIObservationThroughTool(t *testing.T) {
	m, _, in := yieldFixture(t)
	old := phasegate.CheckpointStateRoot
	phasegate.CheckpointStateRoot = t.TempDir()
	t.Cleanup(func() { phasegate.CheckpointStateRoot = old })
	dir := filepath.Dir(checkpointPaths(in.Path, "P13")[0])
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	doc := checkpointDoc{Gates: map[string]checkpointGate{"review": {Status: "PASS", Evidence: "ai:observation"}}}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "P13.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerPhaseGateTool(r, m)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "test", Role: "reader", Scopes: []string{"phase_gate"}})
	v, err := r.readers["phase_gate"](ctx, map[string]any{"path": in.Path, "phase": "P13"})
	if err != nil {
		t.Fatal(err)
	}
	got := v.(PhaseGateResult)
	found := false
	for _, c := range got.Checks {
		if c.Name == "checkpoint" && c.Status != "PASS" {
			found = true
		}
	}
	if got.Verdict == "PASS" || !found {
		t.Fatal(got)
	}
}
