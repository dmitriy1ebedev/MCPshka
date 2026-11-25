package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/phasegate"
)

// Phase gate logic lives in internal/phasegate (0.19 monolith cut); this
// file only registers the two tools.
type (
	PhaseGateInput        = phasegate.PhaseGateInput
	PhaseGateResult       = phasegate.PhaseGateResult
	PhaseGateCheck        = phasegate.PhaseGateCheck
	PhaseCheckpointInput  = phasegate.PhaseCheckpointInput
	PhaseCheckpointOutput = phasegate.PhaseCheckpointOutput
	PhaseCriterion        = phasegate.PhaseCriterion
	checkpointDoc         = phasegate.CheckpointDoc
	checkpointGate        = phasegate.CheckpointGate
)

var (
	evaluatePhaseGate     = phasegate.Evaluate
	recordPhaseCheckpoint = phasegate.Record
	checkpointPaths       = phasegate.CheckpointPaths
	passingGate           = phasegate.PassingGate
)

func init() { phasegate.ServerVersion = Version }

func registerPhaseGateTool(registry *toolRegistry, manager *jobs.Manager) {
	git := gitops.New(manager.Policy())
	closed := false
	addTool(registry, &mcp.Tool{
		Name:        "phase_gate",
		Description: "One verdict (PASS/BLOCKED) for a roadmap phase of any repository: clean Git, no active/UNKNOWN jobs, host pressure per CPU (load ≤1.5/CPU, RAM ≥10%, disk; WARN only defers heavy jobs), a passing sealed test on exact HEAD and, when phase is given, that phase's own checkpoint (recorded with phase_checkpoint_record; every PASS criterion cites a successful job of this project, soak criteria a long enough one). acceptance says what PASS means: CODE_TEST_PASS without phase, PHASE_ACCEPTED with it; a phase without checkpoint is BLOCKED. Act on next_action.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PhaseGateInput) (*mcp.CallToolResult, PhaseGateResult, error) {
		out, err := evaluatePhaseGate(git, manager, in)
		return nil, out, err
	})
	registerPhaseCheckpointTool(registry, manager, git)
}

func registerPhaseCheckpointTool(registry *toolRegistry, manager *jobs.Manager, git *gitops.Service) {
	// Structural admission precedes r.store.Begin. No-effects never create an
	// UNKNOWN idempotency receipt; effectful errors remain fail-closed.
	toolPreflights["phase_checkpoint_record"] = func(_ context.Context, input map[string]any) error {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		var in PhaseCheckpointInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return fmt.Errorf("invalid checkpoint input: %w", err)
		}
		return phasegate.ValidateShape(in)
	}
	closed := false
	addTool(registry, &mcp.Tool{
		Name:        "phase_checkpoint_record",
		Description: "Record a roadmap phase's own acceptance criteria (soak, recovery, neighbour impact…) for phase_gate. Each PASS must cite evidence: job:<id> of a successful MCPshka job in this project (with min_duration_seconds for soak criteria) or knowledge:<id>. Bound to the current HEAD; optional valid_for_hours. Waivers are not accepted (owner decision). A shared project test is never phase evidence by itself.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PhaseCheckpointInput) (*mcp.CallToolResult, PhaseCheckpointOutput, error) {
		out, err := recordPhaseCheckpoint(git, manager, in, time.Now().UTC())
		return nil, out, err
	})
}
