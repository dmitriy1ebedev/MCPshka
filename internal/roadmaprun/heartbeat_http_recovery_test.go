package roadmaprun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHeartbeatReconcilesAndReplaysOnlyOriginalKey(t *testing.T) {
	for _, tc := range []struct {
		name          string
		held, changed bool
	}{
		{name: "held", held: true},
		{name: "released"},
		{name: "stale receipt", held: true, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held := tc.held
			beats, probes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				json.NewDecoder(r.Body).Decode(&req)
				out := map[string]any{}
				switch req.Params.Name {
				case "project_lease_heartbeat":
					out = map[string]any{"held": true, "lease_id": "lease1", "execution_id": "exec1", "generation": 1}
					if tc.changed {
						out["execution_id"] = "old-execution"
					}
					beats++
					if req.Params.Arguments["idempotency_key"] != "original-key" || req.Params.Arguments["token"] != "holder" {
						t.Error("heartbeat identity changed")
					}
					if beats == 1 {
						json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "SQLite state budget initialization failed"}})
						return
					}
				case "project_lease_status":
					probes++
					out = map[string]any{"held": held, "project_root": "/repo", "reconciliation_required": false, "lease_id": "lease1", "execution_id": "exec1", "generation": 1}
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": out}})
			}))
			defer server.Close()
			err := (HTTPLease{URL: server.URL}).Heartbeat(context.Background(), "/repo", "holder", "original-key")
			if probes != 1 || (held && beats != 2) || (!held && beats != 1) || ((held && !tc.changed) != (err == nil)) {
				t.Fatalf("held=%v beats=%d probes=%d err=%v", held, beats, probes, err)
			}
		})
	}
}
