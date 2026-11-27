package brain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactKeepsValidJSONUnderBudget(t *testing.T) {
	cand := map[string]any{"name": "host_triage", "description": strings.Repeat("д", 900), "input_schema": map[string]any{"type": "object"},
		"output_schema": map[string]any{"x": strings.Repeat("y", 4000)}, "constraints": strings.Repeat("z", 2000)}
	var cands []any
	for i := 0; i < 20; i++ {
		cands = append(cands, cand)
	}
	raw, _ := json.Marshal(map[string]any{"candidates": cands, "call_via": strings.Repeat("c", 3000), "code": "OK"})
	out := compact(string(raw), 6000)
	var back map[string]any
	if len(out) > 6000 || json.Unmarshal([]byte(out), &back) != nil {
		t.Fatalf("%d bytes, valid=%v", len(out), json.Valid([]byte(out)))
	}
	if _, noisy := back["call_via"]; noisy || back["code"] != "OK" || !strings.Contains(out, "host_triage") || !strings.Contains(out, "…ещё") {
		t.Fatalf("%s", out[:300])
	}
	if compact("plain text", 3) != "plain text" || compact(`{"a":1}`, 100) != `{"a":1}` {
		t.Fatal("non-JSON or small results must pass unchanged")
	}
}
