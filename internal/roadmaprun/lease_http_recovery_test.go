package roadmaprun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcpshka/internal/failure"
)

func TestAcquireRecoversOnlyProvenCompletedOriginalReceipt(t *testing.T) {
	for _, state := range []string{"handler_completed", "inner-completed", "UNKNOWN", "not_found", "handler_reported_error"} {
		t.Run(state, func(t *testing.T) {
			acquires, probes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				out := map[string]any{}
				switch request.Params.Name {
				case "project_lease_acquire":
					acquires++
					if request.Params.Arguments["idempotency_key"] != "original" || request.Params.Arguments["path"] != "/repo" || request.Params.Arguments["owner"] != "owner" {
						t.Error("acquire identity changed")
					}
					if acquires == 1 {
						json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "lost reply"}})
						return
					}
					out = map[string]any{"token": "original-private-token"}
				case "mutation_probe":
					probes++
					if request.Params.Arguments["key"] != "original" || request.Params.Arguments["tool"] != "project_lease_acquire" {
						t.Error("wrong probe identity")
					}
					out = map[string]any{"outcome": state, "retry_allowed": false}
					if state == "inner-completed" {
						out["handler_receipt"] = "completed"
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": out}})
			}))
			defer server.Close()
			token, err := (HTTPLease{URL: server.URL}).Acquire(context.Background(), "/repo", "owner", "original")
			completed := state == "handler_completed" || state == "inner-completed"
			if probes != 1 || (completed && (err != nil || token != "original-private-token" || acquires != 2)) || (!completed && (err == nil || token != "" || acquires != 1)) {
				t.Fatal(state, token, err, acquires, probes)
			}
		})
	}
}

func TestLeaseHTTPRejectsEmptyAcquireAndUnverifiedRelease(t *testing.T) {
	for _, op := range []string{"acquire", "release"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": map[string]any{}}})
		}))
		lease := HTTPLease{URL: server.URL}
		var err error
		if op == "acquire" {
			_, err = lease.Acquire(context.Background(), "/repo", "owner", "key")
		} else {
			err = lease.Release(context.Background(), "/repo", "token", "key")
		}
		server.Close()
		if err == nil {
			t.Fatal("empty response accepted as verified", op)
		}
	}
}

func TestHTTPLeaseTrustsOnlyVersionedServerNoEffectProof(t *testing.T) {
	for _, tc := range []struct {
		name, schema, class  string
		noEffect, hold, want bool
	}{
		{"verified", "mcpshka.failure/v1", "FAILED_VERIFIED", true, false, true},
		{"unknown", "mcpshka.failure/v1", "FENCED_UNKNOWN", false, true, false},
		{"conflicting", "mcpshka.failure/v1", "FAILED_VERIFIED", true, true, false},
		{"forged unknown", "mcpshka.failure/v1", "FENCED_UNKNOWN", true, false, false},
		{"unknown schema", "other/v1", "FAILED_VERIFIED", true, false, false},
		{"text only", "", "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, probes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Params struct{ Name string } }
				json.NewDecoder(r.Body).Decode(&req)
				result := map[string]any{"structuredContent": map[string]any{}}
				if req.Params.Name == "project_lease_acquire" {
					calls++
					result = map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": "unknown; no_effect=true; refused"}},
						"_meta": map[string]any{"mcpshka/failure": map[string]any{"schema": tc.schema, "class": tc.class, "reason_code": "FIXTURE", "no_effect": tc.noEffect, "hold": tc.hold}}}
				}
				if req.Params.Name == "mutation_probe" {
					probes++
				}
				json.NewEncoder(w).Encode(map[string]any{"result": result})
			}))
			defer server.Close()
			token, err := (HTTPLease{URL: server.URL}).Acquire(context.Background(), "/repo", "owner", "original")
			if err == nil || token != "" || failure.IsNoEffect(err) != tc.want || calls != 1 || tc.want && probes != 0 {
				t.Fatal("wire proof changed retry safety", err, calls, probes)
			}
		})
	}
}
