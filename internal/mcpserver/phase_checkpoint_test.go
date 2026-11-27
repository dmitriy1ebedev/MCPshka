package mcpserver

import (
	"context"
	"encoding/json"
	"mcpshka/internal/phasegate"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func phaseFixture(t *testing.T) (string, *gitops.Service, *jobs.Manager) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "proj")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "p39"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	old := phasegate.CheckpointStateRoot
	phasegate.CheckpointStateRoot = filepath.Join(base, "state")
	t.Cleanup(func() { phasegate.CheckpointStateRoot = old })
	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(base, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	return repo, gitops.New(p), manager
}

func checkStatus(out PhaseGateResult, name string) string {
	for _, c := range out.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return ""
}

// DIAG ACTIVE-02: P39 without its own checkpoint never passes, whatever the
// shared code test said.
func TestPhaseGateWithoutCheckpointIsIncomplete(t *testing.T) {
	repo, git, manager := phaseFixture(t)
	out, err := evaluatePhaseGate(git, manager, PhaseGateInput{Path: repo, Phase: "P39"})
	if err != nil {
		t.Fatal(err)
	}
	if checkStatus(out, "checkpoint") != "MISSING" || out.Verdict != "BLOCKED" || out.Acceptance != "INCOMPLETE" {
		t.Fatalf("missing checkpoint passed: %+v", out)
	}
	if !strings.Contains(strings.Join(out.Blockers, " "), "phase_checkpoint_record") {
		t.Fatalf("no route to record the phase: %v", out.Blockers)
	}
}

func TestPhaseCheckpointRejectsPassWithoutEvidenceAndWaivers(t *testing.T) {
	repo, git, manager := phaseFixture(t)
	now := time.Now().UTC()
	for _, c := range []PhaseCriterion{
		{Name: "soak_7d", Status: "PASS"},
		{Name: "soak_7d", Status: "PASS", Evidence: "job:nope"},
		{Name: "soak_7d", Status: "PASS", Evidence: "knowledge:k1", MinDurationSeconds: 604800},
		{Name: "soak_7d", Status: "WAIVED", Evidence: "knowledge:k1"},
		{Name: "Bad Name", Status: "PENDING"},
	} {
		if _, err := recordPhaseCheckpoint(git, manager, PhaseCheckpointInput{Path: repo, Phase: "P39", Criteria: []PhaseCriterion{c}}, now); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	out, err := recordPhaseCheckpoint(git, manager, PhaseCheckpointInput{Path: repo, Phase: "P39", Criteria: []PhaseCriterion{{Name: "soak_7d", Status: "PENDING"}, {Name: "notes", Status: "PASS", Evidence: "knowledge:diag-p39"}}, ValidForHours: 24}, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.Gates["soak_7d"] != "PENDING" || out.ExpiresAt == "" {
		t.Fatalf("record %+v", out)
	}
	gate, err := evaluatePhaseGate(git, manager, PhaseGateInput{Path: repo, Phase: "P39"})
	if err != nil {
		t.Fatal(err)
	}
	if checkStatus(gate, "checkpoint") != "FAIL" || gate.Acceptance != "INCOMPLETE" {
		t.Fatalf("pending soak accepted: %+v", gate)
	}
}

func TestPhaseCheckpointExpiryAndForeignHead(t *testing.T) {
	repo, git, manager := phaseFixture(t)
	path := checkpointPaths(repo, "P39")[0]
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(doc map[string]any) {
		raw, _ := json.Marshal(doc)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"version": 2, "expires_at": time.Now().Add(-time.Hour), "gates": map[string]any{"x": map[string]any{"status": "PASS", "evidence": "knowledge:k"}}})
	if out, _ := evaluatePhaseGate(git, manager, PhaseGateInput{Path: repo, Phase: "P39"}); checkStatus(out, "checkpoint") != "FAIL" {
		t.Fatalf("expired checkpoint passed: %+v", out.Checks)
	}
	write(map[string]any{"version": 2, "head": strings.Repeat("ab", 20), "gates": map[string]any{"x": map[string]any{"status": "PASS", "evidence": "knowledge:k"}}})
	if out, _ := evaluatePhaseGate(git, manager, PhaseGateInput{Path: repo, Phase: "P39"}); checkStatus(out, "checkpoint") != "FAIL" {
		t.Fatalf("foreign head passed: %+v", out.Checks)
	}
	write(map[string]any{"version": 2, "gates": map[string]any{"x": map[string]any{"status": "PASS", "evidence": "knowledge:k"}}})
	if out, _ := evaluatePhaseGate(git, manager, PhaseGateInput{Path: repo, Phase: "P39"}); checkStatus(out, "checkpoint") != "PASS" {
		t.Fatalf("valid checkpoint rejected: %+v", out.Checks)
	}
}

func TestPhaseCheckpointMalformedCriterionRejectedBeforeReceiptMCP134(t *testing.T) {
	// A structurally invalid criterion used to fail inside the writer, after
	// r.store.Begin, leaving an UNKNOWN/PENDING receipt for a no-effect call.
	c := catalogClient(t, nil)
	key := "mcp134-invalid-then-corrected-criterion"
	call := func(name string) string {
		t.Helper()
		out, err := c.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "phase_checkpoint_record",
			Arguments: map[string]any{
				"path": "/root/repos/diag", "phase": "P39", "idempotency_key": key,
				"criteria": []any{map[string]any{"name": name, "status": "PENDING"}},
			},
		})
		if err != nil {
			return err.Error()
		}
		if out == nil {
			return ""
		}
		var messages []string
		for _, part := range out.Content {
			if plain, ok := part.(*mcp.TextContent); ok {
				messages = append(messages, plain.Text)
			}
		}
		return strings.Join(messages, "\n")
	}
	bad := call("measured_B_T_N_F_acceptance")
	if !strings.Contains(bad, "lowercase id") {
		t.Fatalf("invalid criterion not rejected with typed validation: %s", bad)
	}
	// Changing the payload under the same key must not conflict with a
	// stored receipt from a rejected call. A path error is acceptable here:
	// catalogClient confines projects to its own ephemeral policy root.
	good := call("measured_btnf_acceptance")
	for _, forbidden := range []string{"outcome UNKNOWN", "idempotency key already used", "idempotency key reused", "idempotency fingerprint", "FENCED_UNKNOWN"} {
		if strings.Contains(good, forbidden) {
			t.Fatalf("invalid criterion allocated a pending receipt: %s", good)
		}
	}
}

func TestPhaseCheckpointShapeRejectsAllInvalidCriteriaBeforeIO(t *testing.T) {
	base := PhaseCheckpointInput{Phase: "P39", Criteria: []PhaseCriterion{{Name: "soak_7d", Status: "PENDING"}}}
	if err := phasegate.ValidateShape(base); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []PhaseCheckpointInput{
		{Phase: "P39", Criteria: []PhaseCriterion{{Name: "Upper_Criterion", Status: "PENDING"}}},
		{Phase: "P39", Criteria: []PhaseCriterion{{Name: "soak_7d", Status: "WAIVED"}}},
		{Phase: "P39", Criteria: []PhaseCriterion{{Name: "soak_7d", Status: "PENDING", MinDurationSeconds: -1}}},
		{Phase: "P39", Criteria: []PhaseCriterion{{Name: "soak_7d", Status: "PENDING", Note: strings.Repeat("n", 301)}}},
		{Phase: "P39", Criteria: []PhaseCriterion{{Name: "soak_7d", Status: "PENDING"}}, ValidForHours: 2161},
	} {
		if err := phasegate.ValidateShape(bad); err == nil {
			t.Fatalf("accepted invalid shape: %+v", bad)
		}
	}
}
