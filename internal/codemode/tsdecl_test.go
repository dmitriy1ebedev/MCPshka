package codemode

import "testing"

func TestTSDeclarationCoversSchemaShapes(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"path"}, "properties": map[string]any{
		"path":  map[string]any{"type": "string", "description": "File  inside\nroot."},
		"limit": map[string]any{"type": "integer"},
		"mode":  map[string]any{"type": "string", "enum": []any{"content", "name"}},
		"files": map[string]any{"type": []any{"null", "array"}, "items": map[string]any{"type": "object", "properties": map[string]any{"sha256": map[string]any{"type": "string"}}}},
		"env":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"any":   map[string]any{},
	}}
	got := Declaration("fs_find", schema)
	want := `declare function fs_find(args: {
  any?: any;
  env?: Record<string, string>;
  files?: null | {
    sha256?: string;
  }[];
  limit?: number;
  mode?: "content" | "name";
  /** File inside root. */
  path: string;
}): Promise<any>;`
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
}
