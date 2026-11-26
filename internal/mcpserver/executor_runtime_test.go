package mcpserver

import (
	"context"
	"strings"
	"testing"
)

func TestReadonlyWorkerCompatProbeUsesRootRuntimeBeforeAndAfter(t *testing.T) {
	permit, launch := strings.Repeat("a", 32), strings.Repeat("b", 32)
	calls := 0
	r := &toolRegistry{readers: map[string]probeHandler{
		"executor_runtime_status": func(_ context.Context, in map[string]any) (any, error) {
			calls++
			if in["permit_id"] != permit || in["launch_id"] != launch || in["workdir"] != "/root/repos/mcpshka" {
				t.Fatalf("probe changed identities: %v", in)
			}
			return map[string]any{"state": "REVOKING"}, nil
		},
	}}
	in := map[string]any{"command": "mcpshka", "args": []string{"executor-canary", "canary-recover", permit, launch}, "workdir": "/root/repos/mcpshka", "idempotency_key": "retry"}
	for _, before := range []bool{true, false} {
		got, err := r.evidence(context.Background(), "exec_start", in, map[string]any{"id": "transport-receipt-done"}, before)
		if err != nil || got.Probe != "executor_runtime_status" {
			t.Fatalf("receipt replaced runtime %v %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatal("root runtime not observed")
	}
}
