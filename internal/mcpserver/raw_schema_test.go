package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"mcpshka/internal/jobs"
)

func TestJobOutputSchemaAcceptsRawExternalBinding(t *testing.T) {
	s, err := jsonschema.For[ProjectTestResult](&jsonschema.ForOptions{TypeSchemas: rawJSONSchemas})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := ProjectTestResult{Job: jobs.Snapshot{ID: "j", External: &jobs.ExternalBinding{Runtime: "r", Binding: json.RawMessage(`{"schema":"x","pin":{"permit_id":"p"}}`)}}}
	raw, _ := json.Marshal(out)
	var instance map[string]any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatal("object binding rejected by output schema:", err)
	}
}
