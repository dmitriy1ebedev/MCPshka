package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"mcpshka/internal/executor"
	"mcpshka/internal/jobs"
	"mcpshka/internal/pipeline"
)

// MCP-204: the direct tool's declared output schema must accept the *actual*
// embedded pipeline manifest, including new 0.13.4 artifact and attempt fields.
// This exercises the same schema a strict Claude MCP client validates and
// prevents the tool-invoke wrapper from hiding a server schema mismatch.
func TestMCP204DirectPipelineOutputSchemaRichReceiptFixtures(t *testing.T) {
	client := fullCatalogClient(t)
	var schemaRaw any
	for _, tool := range catalogTools(t, client) {
		if tool.Name == "project_pipeline" {
			schemaRaw = tool.OutputSchema
			break
		}
	}
	if schemaRaw == nil {
		t.Fatal("project_pipeline has no direct OutputSchema")
	}
	encoded, err := json.Marshal(schemaRaw)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	validator, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	terminal := 0
	for _, tc := range []struct {
		state         string
		hasJob        bool
		withArtifacts bool
	}{
		{"PASS", false, true},
		{"RUNNING", true, false},
		{"FAIL", true, true},
		{"QUEUED", true, false},
		{"PASS", false, false},
	} {
		t.Run(tc.state+map[bool]string{true: "-job", false: "-receipt"}[tc.hasJob]+
			map[bool]string{true: "-artifact", false: "-empty"}[tc.withArtifacts], func(t *testing.T) {
			m := pipeline.PipelineManifest{
				Schema: "mcpshka.pipeline/v1", Project: "/root/repos/ug2",
				HEAD: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40),
				Profile: "ug2", ProfileDigest: strings.Repeat("c", 64),
				Stage: "package", Command: []string{"python3", "tools/package.py"},
				State: tc.state, PermitID: "p-receipt", JobID: "mw-example",
				StartedAt: t0, FinishedAt: t0.Add(time.Minute), Attempt: 2,
				ManifestPath: "dist/package-manifest.json",
			}
			if tc.withArtifacts {
				m.Artifacts = []executor.PipelineArtifact{{
					Path: "dist/UG2-0.17.0.zip", SHA256: strings.Repeat("d", 64), Bytes: 19871878,
				}}
			}
			result := pipeline.Result{State: tc.state, NextStage: "verify",
				Evidence: []pipeline.PipelineEvidence{{PipelineManifest: m, Reused: false}}}
			if tc.hasJob {
				result.Job = &jobs.Snapshot{ID: "mw-example", Command: "python3", Args: []string{"tools/package.py"},
					Workdir: "/root/repos/ug2", State: jobs.Running, StartedAt: t0,
					TimeoutSeconds: 600, OutputLimitBytes: 16384, MemoryLimitBytes: 536870912,
					CPUTimeLimitSeconds: 120, ProcessLimit: 24}
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var parsed any
			if err = json.Unmarshal(raw, &parsed); err != nil {
				t.Fatal(err)
			}
			if err = validator.Validate(parsed); err != nil {
				t.Fatalf("direct MCP project_pipeline %s receipt failed strict output contract: %v; payload=%s",
					tc.state, err, string(raw))
			}
			terminal++
		})
	}
	if terminal != 5 {
		t.Fatalf("expected 5 contract fixtures, got %d", terminal)
	}
}
