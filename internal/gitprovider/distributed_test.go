package gitprovider

import (
	"context"
	"encoding/json"
	"mcpshka/internal/distributedci"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCIProbeQuotaUnknownAndExhausted(t *testing.T) {
	for _, body := range []string{"{}", "{\"included_minutes\":2000,\"total_minutes_used\":1990}", "{\"included_minutes\":2000,\"total_minutes_used\":10}"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/user":
				w.Write([]byte("{\"login\":\"dmitriy1ebedev\"}"))
			case "/repos/dmitriy1ebedev/mcpshka-ci/actions/permissions":
				w.Write([]byte("{\"enabled\":true}"))
			case "/users/dmitriy1ebedev/settings/billing/actions":
				w.Write([]byte(body))
			default:
				w.Write([]byte("{}"))
			}
		}))
		c := &Client{site: GitHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		p := CIProvider{Client: c}
		e := p.Probe(context.Background())
		if (e == nil) != strings.Contains(body, "used\":10") {
			t.Fatal(body, e)
		}
		s.Close()
	}
}
func TestParseCIResultRejectsDuplicateAndUnknownFields(t *testing.T) {
	o := distributedci.Observation{Provider: "github", State: "PASS", RunID: 4}
	b, _ := json.Marshal(o)
	raw := []byte(time.Now().Format(time.RFC3339) + " " + distributedci.ResultMarker + string(b) + "\n")
	if _, e := ParseCIResult(raw); e != nil {
		t.Fatal(e)
	}
	if _, e := ParseCIResult(append(raw, raw...)); e == nil {
		t.Fatal("duplicate accepted")
	}
	if _, e := ParseCIResult([]byte(distributedci.ResultMarker + "{\"unknown\":true}")); e == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestCIDownloadRejectsRedirectWithoutCredentialLeak(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://evil.invalid/log")
		w.WriteHeader(302)
	}))
	defer s.Close()
	c := &Client{site: GitHub, base: s.URL, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, token: func(Site) (string, error) { return "fixture", nil }}
	if _, e := c.ciRequest(context.Background(), "GET", "/jobs/1/logs", nil); e == nil {
		t.Fatal("untrusted download allowed")
	}
}

func TestCIObserveBindsSnapshotAndRetainsCodeFailure(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []bool{false, true} {
		snapshot := p.Snapshot
		if bad {
			snapshot = p.Commit
		}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/distributed.yml/runs"):
				json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{{"id": 11, "head_sha": snapshot, "head_branch": distributedci.Ref(p)}}})
			case strings.HasSuffix(r.URL.Path, "/runs/11/jobs"):
				json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": 12, "name": "distributed-shard-0", "status": "completed", "conclusion": "success"}}})
			case strings.HasSuffix(r.URL.Path, "/jobs/12/logs"):
				o := distributedci.Observation{Provider: "github", Shard: 0, Commit: p.Commit, Tree: p.Tree, Snapshot: p.Snapshot, PlanHash: p.Hash(), Packages: p.Shards[0], State: "PASS", RunID: 11}
				b, _ := json.Marshal(o)
				w.Write([]byte(distributedci.ResultMarker + string(b) + "\n"))
			default:
				t.Errorf("unexpected route %s", r.URL.Path)
				w.WriteHeader(404)
			}
		}))
		c := &Client{site: GitHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		adapter := CIProvider{Client: c}
		o, found, e := adapter.Observe(context.Background(), p, 0)
		if bad {
			if e == nil {
				t.Fatal("snapshot substitution accepted")
			}
		} else if e != nil || !found || o.State != "PASS" {
			t.Fatal(o, found, e)
		}
		s.Close()
	}
}

func TestCIModernQuotaAPIAndReservation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/billing/actions") {
			w.WriteHeader(403)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/billing/usage/summary") {
			json.NewEncoder(w).Encode(map[string]any{"usageItems": []map[string]any{{"product": "Actions", "sku": "actions_linux", "unitType": "minutes", "grossQuantity": 100}, {"product": "Actions", "sku": "actions_windows", "unitType": "minutes", "grossQuantity": 20}}})
			return
		}
		t.Errorf("unexpected quota route %s", r.URL.Path)
		w.WriteHeader(404)
	}))
	defer s.Close()
	c := &Client{site: GitHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
	remaining, e := c.freeCIMinutes(context.Background())
	if e != nil || remaining != 1860 {
		t.Fatal(remaining, e)
	}
	p := CIProvider{Client: c, quotaRemaining: 49}
	if e := p.Start(context.Background(), distributedci.Plan{}, 0); e == nil || p.quotaRemaining != 49 {
		t.Fatal("unfunded dispatch allowed", e)
	}
}

func TestCIQueuedRunTimesOutAsInfrastructure(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	for _, age := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/distributed.yml/runs") {
				json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{{"id": 11, "head_sha": p.Snapshot, "head_branch": distributedci.Ref(p), "created_at": time.Now().Add(-age)}}})
				return
			}
			if strings.HasSuffix(r.URL.Path, "/runs/11/jobs") {
				json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": 12, "name": "distributed-shard-0", "status": "queued"}}})
				return
			}
			t.Errorf("queued job requested logs: %s", r.URL.Path)
			w.WriteHeader(404)
		}))
		c := &Client{site: GitHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		adapter := CIProvider{Client: c}
		o, ok, e := adapter.Observe(context.Background(), p, 0)
		want := "QUEUED"
		if age > 15*time.Minute {
			want = "INFRA_FAIL"
		}
		if e != nil || !ok || o.State != want || p.Check(o) != nil {
			t.Fatal(o, e)
		}
		s.Close()
	}
}

func TestCIHostCancellationAndTimeoutFailOver(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	for _, conclusion := range []string{"cancelled", "timed_out", "startup_failure", "action_required"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/distributed.yml/runs"):
				json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{{"id": 11, "head_sha": p.Snapshot, "head_branch": distributedci.Ref(p)}}})
			case strings.HasSuffix(r.URL.Path, "/runs/11/jobs"):
				json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": 12, "name": "distributed-shard-0", "status": "completed", "conclusion": conclusion}}})
			case strings.HasSuffix(r.URL.Path, "/jobs/12/logs"):
				w.Write([]byte("runner stopped before checks\n"))
			default:
				t.Errorf("unexpected route %s", r.URL.Path)
				w.WriteHeader(404)
			}
		}))
		c := &Client{site: GitHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		adapter := CIProvider{Client: c}
		o, ok, e := adapter.Observe(context.Background(), p, 0)
		if e != nil || !ok || o.State != "INFRA_FAIL" {
			t.Fatal(conclusion, o, e)
		}
		s.Close()
	}
}

func TestMosHubProbeReadsRunnerDetailsForTags(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Write([]byte("{\"username\":\"lda6994\"}"))
		case "/projects/91385":
			w.Write([]byte("{}"))
		case "/projects/91385/runners":
			w.Write([]byte("[{\"id\":42,\"status\":\"online\",\"paused\":false}]"))
		case "/runners/42":
			w.Write([]byte("{\"id\":42,\"status\":\"online\",\"paused\":false,\"tag_list\":[\"appsechub\"]}"))
		default:
			t.Errorf("unexpected runner route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := &Client{site: MosHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
	if e := (&CIProvider{Client: c}).Probe(context.Background()); e != nil {
		t.Fatal(e)
	}
}
