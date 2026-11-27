package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"mcpshka/internal/pipeline"
)

func TestProjectPipelineOutputSchemaRejectsNoRealFields(t *testing.T) {
	c := fullCatalogClient(t)
	var rawSchema any
	for _, tool := range catalogTools(t, c) {
		if tool.Name == "project_pipeline" {
			rawSchema = tool.OutputSchema
			break
		}
	}
	if rawSchema == nil {
		t.Fatal("pipeline output schema absent")
	}
	bytes, err := json.Marshal(rawSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(bytes, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"RUNNING", "PASS", "FAIL"} {
		v := pipeline.Result{State: state, Evidence: []pipeline.PipelineEvidence{{
			PipelineManifest: pipeline.PipelineManifest{
				Schema: "mcpshka.pipeline/v1", Project: "/root/repos/ug2", HEAD: "sha",
				Tree: "tree", ProfileDigest: "digest", Stage: "package", Command: []string{"python3", "tools/update_pack.py"},
				State: state, PermitID: "permit", Attempt: 2, ManifestPath: "dist/manifest.json",
			}, Reused: false,
		}}}
		b, _ := json.Marshal(v)
		var item any
		if err := json.Unmarshal(b, &item); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(item); err != nil {
			t.Fatalf("pipeline %s: %v; schema=%s; output=%s", state, err, string(bytes), string(b))
		}
	}
}
