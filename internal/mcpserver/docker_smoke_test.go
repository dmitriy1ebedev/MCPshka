package mcpserver

import (
	"context"
	"testing"
)

// BUG-20261024-105: the before-probe asked docker_smoke_status for an id that
// only the broker derives, so every docker_smoke start was refused.
func TestDockerSmokeProbes(t *testing.T) {
	var seen []string
	r := &toolRegistry{readers: map[string]probeHandler{
		"state_status": func(_ context.Context, args map[string]any) (any, error) {
			seen = append(seen, "state")
			return map[string]any{"ok": true}, nil
		},
		"docker_smoke_status": func(_ context.Context, args map[string]any) (any, error) {
			if args["id"] != "abc" {
				t.Fatalf("after probe got %+v", args)
			}
			seen = append(seen, "status")
			return map[string]any{"state": "QUEUED"}, nil
		},
	}}
	in := map[string]any{"path": "/root/repos/diag", "idempotency_key": "k"}
	if _, err := r.evidence(context.Background(), "docker_smoke", in, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.evidence(context.Background(), "docker_smoke", in, map[string]any{"id": "abc"}, false); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "state" || seen[1] != "status" {
		t.Fatalf("probes %v", seen)
	}
}
