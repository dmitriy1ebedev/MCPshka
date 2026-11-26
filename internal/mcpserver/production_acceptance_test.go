package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/evidence"
)

func TestReleaseAcceptanceAddsGatesWithoutWaivingRoadmap(t *testing.T) {
	old := evidence.Dir
	evidence.Dir = t.TempDir()
	defer func() { evidence.Dir = old }()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ROADMAP.md"), []byte("# Plan\n## P1\nGates: build\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := evidence.Put(evidence.Record{Project: root, Gate: "build", Result: "PASS", Source: "fixture"}); err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	registerAcceptance(r)
	_, plain, err := r.invokers["acceptance_status"](context.Background(), map[string]any{"path": root, "step": "P1"})
	if err != nil || !plain.(AcceptanceResult).Done {
		t.Fatal("fixture build not accepted", plain, err)
	}
	_, strict, err := r.invokers["acceptance_status"](context.Background(), map[string]any{"path": root, "step": "P1", "required_gates": []string{"release", "cleanup"}})
	if err != nil {
		t.Fatal(err)
	}
	out := strict.(AcceptanceResult)
	if out.Done || len(out.Gates) != 3 || out.Gates[0].Gate != "build" {
		t.Fatal("release criteria waived original gate", out)
	}
	if _, _, err := r.invokers["acceptance_status"](context.Background(), map[string]any{"path": root, "step": "P1", "required_gates": []string{"invented"}}); err == nil {
		t.Fatal("unknown gate accepted")
	}
}
