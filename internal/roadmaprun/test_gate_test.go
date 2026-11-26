package roadmaprun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPTestOwnsCleanHeadSealedJob(t *testing.T) {
	for _, tc := range []struct {
		name, state       string
		profile           string
		dirty, move       bool
		builtin, unsealed bool
		want              string
	}{
		{name: "success", state: "done"}, {name: "unknown then done", state: "unknown-once"}, {name: "disconnect then done", state: "disconnect-once"}, {name: "failure", state: "failed", want: "admission unverified"}, {name: "dirty", state: "done", dirty: true, want: "clean HEAD"}, {name: "moved", state: "done", move: true, want: "changed"}, {name: "unknown", state: "unknown", want: "UNKNOWN"},
		{name: "builtin", state: "done", builtin: true, want: "project_onboard"},
		{name: "legacy builtin", state: "done", builtin: true, profile: "diag", want: "project_onboard"},
		{name: "unsealed", state: "done", unsealed: true, want: "not bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contexts, tests, started, waits := 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
					Params struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				var out any = map[string]any{}
				switch req.Params.Name {
				case "project_context":
					contexts++
					head := "h1"
					if contexts > 1 && tc.move {
						head = "h2"
					}
					profile := tc.profile
					if profile == "" {
						profile = "example"
					}
					source := "registered"
					if tc.builtin {
						source = "builtin"
					}
					out = map[string]any{"profile": profile, "profile_source": source, "revision": head, "git": map[string]any{"dirty": tc.dirty}}
				case "project_test":
					tests++
					if _, ok := req.Params.Arguments["packages"]; ok {
						t.Error("unsealed test")
					}
					if req.Params.Arguments["idempotency_key"] != "key" || req.Params.Arguments["lease_token"] != "token" {
						t.Error("identity lost")
					}
					job := map[string]any{"id": "job1"}
					if !tc.unsealed {
						job["external"] = map[string]any{"runtime": "mcpshka-workbench/v1"}
					}
					out = map[string]any{"job": job}
				case "job_wait":
					waits++
					state := tc.state
					if state == "unknown-once" {
						state = "unknown"
						if waits > 1 {
							state = "done"
						}
					}
					if state == "disconnect-once" {
						if waits == 1 {
							w.WriteHeader(503)
							return
						}
						state = "done"
					}
					out = map[string]any{"status": map[string]any{"state": state, "exit_code": 0, "resources_released": true}, "stderr": "admission unverified"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": out}})
			}))
			defer server.Close()
			ctx := context.Background()
			if tc.state == "unknown" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			job, head, next, err := HTTPTest(server.URL, "")(ctx, "/p", "token", "key", func(id string) error {
				started++
				if id != "job1" {
					t.Error(id)
				}
				return nil
			})
			if tc.want == "" {
				if err != nil || next != "" || job != "job1" || head != "h1" || tests != 1 || started != 1 {
					t.Fatalf("%q %q %q %v", job, head, next, err)
				}
			} else if !strings.Contains(next+" "+func() string {
				if err != nil {
					return err.Error()
				}
				return ""
			}(), tc.want) {
				t.Fatalf("next=%s err=%v", next, err)
			}
		})
	}
}

func TestHTTPTestRecoversLostAdmissionReply(t *testing.T) {
	for _, mode := range []string{"sealed", "missing", "probe-error", "status-error", "unsealed", "wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			tests, probes, statuses, waits, starts := 0, 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Params struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				var out any = map[string]any{}
				switch req.Params.Name {
				case "project_context":
					out = map[string]any{"profile_source": "registered", "revision": "h1", "git": map[string]any{"dirty": false}}
				case "project_test":
					tests++
					w.WriteHeader(503) // Admission may have succeeded; its response was lost.
					return
				case "mutation_probe":
					probes++
					if req.Params.Arguments["tool"] != "project_test" || req.Params.Arguments["key"] != "key" {
						t.Error("original mutation identity lost")
					}
					if mode == "probe-error" {
						w.WriteHeader(503)
						return
					}
					out = map[string]any{"outcome": "UNKNOWN", "retry_allowed": false}
					if mode != "missing" {
						out.(map[string]any)["handles"] = map[string]any{"job_id": "job1"}
					}
				case "job_status":
					statuses++
					if req.Params.Arguments["job_id"] != "job1" || req.Params.Arguments["verbose"] != true {
						t.Error("recovery must inspect the same job including its runtime binding")
					}
					if mode == "status-error" {
						w.WriteHeader(503)
						return
					}
					id := "job1"
					if mode == "wrong-id" {
						id = "foreign"
					}
					job := map[string]any{"id": id}
					if mode != "unsealed" {
						job["external"] = map[string]any{"runtime": "mcpshka-workbench/v1"}
					}
					out = job
				case "job_wait":
					waits++
					if req.Params.Arguments["job_id"] != "job1" {
						t.Error("observing a different job")
					}
					out = map[string]any{"status": map[string]any{"state": "done", "exit_code": 0, "resources_released": true}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": out}})
			}))
			defer server.Close()
			job, head, _, err := HTTPTest(server.URL, "")(context.Background(), "/p", "token", "key", func(id string) error {
				starts++
				if id != "job1" {
					t.Error("checkpoint lost recovered job")
				}
				return nil
			})
			if tests != 1 || probes != 1 {
				t.Fatalf("admission must be probed, never duplicated: tests=%d probes=%d err=%v", tests, probes, err)
			}
			if mode == "sealed" {
				if err != nil || job != "job1" || head != "h1" || waits != 1 || starts != 1 || statuses != 1 {
					t.Fatalf("receipt did not resume sealed observation: %q %q %v waits=%d starts=%d statuses=%d", job, head, err, waits, starts, statuses)
				}
			} else if err == nil || waits != 0 || head != "" {
				t.Fatalf("unverified recovery accepted: job=%q head=%q err=%v waits=%d", job, head, err, waits)
			}
		})
	}
}
