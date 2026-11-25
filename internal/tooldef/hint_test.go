package tooldef

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSchemaHintNamesTheRightCall(t *testing.T) {
	s := &jsonschema.Schema{Type: "object", Required: []string{"path", "expected_version"}, Properties: map[string]*jsonschema.Schema{
		"path": {Type: "string"}, "expected_version": {Type: "string"}, "limit": {Type: "integer"}, "text": {Type: "string"},
	}}
	got := SchemaHint(s, map[string]any{"pth": "/x", "version": "0.18.0", "query": "q"})
	for _, want := range []string{"«pth» → «path»", "«version» → «expected_version»", "required: path, expected_version", `example: {"expected_version":"…","path":"…"}`, "accepted: expected_version, limit, path, text"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
