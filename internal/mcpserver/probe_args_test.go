package mcpserver

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP-132: phase_checkpoint_record probes phase_gate, which needs path and
// phase; without a hand-written case the probe ran with no arguments.
func TestProbeArgsFilledFromProbeSchema(t *testing.T) {
	schema := func(required ...string) map[string]any {
		props := map[string]any{}
		for _, k := range []string{"path", "phase", "idempotency_key", "criteria"} {
			props[k] = map[string]any{"type": "string"}
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	r := &toolRegistry{catalog: map[string]*mcp.Tool{
		"phase_gate":              {Name: "phase_gate", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{}, "phase": map[string]any{}}, "required": []string{"path"}}},
		"phase_checkpoint_record": {Name: "phase_checkpoint_record", InputSchema: schema("path", "phase")},
	}}
	in := map[string]any{"path": "/root/repos/diag", "phase": "P39", "idempotency_key": "k", "criteria": "x"}
	args := r.fillProbeArgs("phase_gate", map[string]any{}, in)
	if args["path"] != "/root/repos/diag" || args["phase"] != "P39" || args["idempotency_key"] != nil || args["criteria"] != nil {
		t.Fatalf("probe args %v", args)
	}
	// An explicit mapping wins over the schema fill.
	if got := r.fillProbeArgs("phase_gate", map[string]any{"path": "/other"}, in); got["path"] != "/other" {
		t.Fatalf("explicit mapping overwritten: %v", got)
	}
	if gaps := r.probeGaps("phase_checkpoint_record", "phase_gate", args); len(gaps) != 0 {
		t.Fatalf("gaps %v", gaps)
	}
	r.catalog["lonely"] = &mcp.Tool{Name: "lonely", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{}}}}
	if gaps := r.probeGaps("lonely", "phase_gate", map[string]any{}); len(gaps) != 1 || gaps[0] != "path" {
		t.Fatalf("gap not reported: %v", gaps)
	}
}

// MCP-137: integration_worker_fence probes integration_worker_status; the
// mutation's action must not reach the probe, which refuses a foreign one.
func TestProbeArgsSkipMutationAction(t *testing.T) {
	props := map[string]any{"action": map[string]any{}, "permit_id": map[string]any{}}
	r := &toolRegistry{catalog: map[string]*mcp.Tool{
		"integration_worker_status": {Name: "integration_worker_status", InputSchema: map[string]any{"type": "object", "properties": props}},
	}}
	args := r.fillProbeArgs("integration_worker_status", map[string]any{}, map[string]any{"action": "fence", "permit_id": "p"})
	if _, set := args["action"]; set || args["permit_id"] != "p" {
		t.Fatalf("probe args %v", args)
	}
}
