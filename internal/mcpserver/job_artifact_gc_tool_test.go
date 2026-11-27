package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestJobArtifactGCToolPreviewAndApprovalGuard(t *testing.T) {
	ctx := context.WithValue(context.Background(), fullCatalogKey{}, true)
	root := t.TempDir()
	pol := policy.Default()
	pol.AllowedWorkdirs = []string{root}
	manager, err := jobs.New(t.TempDir(), pol)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	server := New(manager, filesystem.New(pol))
	// The isolated test server has no durable lease registry. Supply the
	// available empty reference snapshot explicitly; a failed registry
	// remains covered by the jobs package's fail-closed regression.
	manager.SetReapReferences(func() (map[string]bool, error) {
		return map[string]bool{}, nil
	})
	manager.SetLeaseGuard(func(string, string) error { return nil })
	client := mcp.NewClient(&mcp.Implementation{Name: "artifact-gc-test", Version: "1"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundPreview, foundApply := false, false
	for _, tool := range listed.Tools {
		if tool.Name == "job_artifact_gc_preview" {
			foundPreview = true
		}
		if tool.Name == "job_artifact_gc_apply" {
			foundApply = true
		}
	}
	if !foundPreview || !foundApply {
		t.Fatal("job artifact GC read-only preview or Face ID apply missing")
	}
	preview, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "job_artifact_gc_preview", Arguments: map[string]any{}})
	if err != nil || preview.IsError {
		t.Fatalf("preview rejected: %+v %v", preview, err)
	}
	raw, err := json.Marshal(preview.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var report jobs.ArtifactGCReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "preview" || len(report.PlanSHA256) != 64 || report.Deleted != 0 {
		t.Fatalf("unsafe preview: %+v", report)
	}
	// Required in the mutation schema: the caller cannot turn a preview
	// into a cleanup by presenting a digest without a Face ID session.
	apply, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "job_artifact_gc_apply", Arguments: map[string]any{
		"plan_sha256": report.PlanSHA256, "idempotency_key": "gc-no-faceid",
	}})
	if err == nil && !apply.IsError {
		t.Fatalf("unapproved deletion admitted: %+v", apply)
	}
	bad, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "job_artifact_gc_apply", Arguments: map[string]any{}})
	if err == nil && !bad.IsError {
		t.Fatalf("unpinned deletion admitted: %+v", bad)
	}
}
