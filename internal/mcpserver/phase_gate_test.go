package mcpserver

import (
	"mcpshka/internal/hosttriage"
	"mcpshka/internal/phasegate"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestPhaseGateUsesCheckpointFactsAndRequiresSealedTest(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "diag")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "p02"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	old := phasegate.CheckpointStateRoot
	phasegate.CheckpointStateRoot = filepath.Join(base, "state")
	defer func() { phasegate.CheckpointStateRoot = old }()
	dir := filepath.Join(phasegate.CheckpointStateRoot, "diag-checkpoints")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	checkpoint := `{"gates":{"go_test":"PASS","drive":"WAIVED_BY_USER_2026-10-19","pressure":"PASS root deploy","git_integrated_main":"PENDING","roadmap_done":"PENDING"}}`
	if err := os.WriteFile(filepath.Join(dir, "P02.json"), []byte(checkpoint), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(base, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := evaluatePhaseGate(gitops.New(p), manager, PhaseGateInput{Path: repo, Phase: "P02"})
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, c := range out.Checks {
		status[c.Name] = c.Status
	}
	if status["checkpoint"] != "PASS" || status["git_clean"] != "PASS" || status["project_jobs"] != "PASS" {
		t.Fatalf("checkpoint facts misjudged: %+v", out.Checks)
	}
	if status["sealed_test"] != "MISSING" || out.Verdict != "BLOCKED" || !strings.Contains(out.NextAction, "project_test") {
		t.Fatalf("missing sealed test not reported: %+v", out)
	}
	if !strings.Contains(hosttriage.Pressure().Detail, "per CPU") {
		t.Fatal("pressure must be reported per CPU")
	}
	if !passingGate("WAIVED_BY_USER") || passingGate("PENDING") || passingGate("FAIL") {
		t.Fatal("gate classification")
	}
	if _, err := evaluatePhaseGate(gitops.New(p), manager, PhaseGateInput{Path: repo, Phase: "../x"}); err == nil {
		t.Fatal("path-like phase accepted")
	}
}
