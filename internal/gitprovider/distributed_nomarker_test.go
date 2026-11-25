package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/distributedci"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 18.11: GitHub jobs died before the shard ran (sudo dropped setup-go from
// PATH); without a result line the whole plan sat in HOLD instead of moving
// the shard to another site.
func TestCIFailedJobWithoutResultMovesShard(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c"}, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		conclusion string
		want       string
	}{{"failure", "INFRA_FAIL"}, {"success", ""}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/distributed.yml/runs"):
				json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{{"id": 11, "head_sha": p.Snapshot, "head_branch": distributedci.Ref(p)}}})
			case strings.HasSuffix(r.URL.Path, "/runs/11/jobs"):
				json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": 12, "name": "distributed-shard-0", "status": "completed", "conclusion": tc.conclusion}}})
			case strings.HasSuffix(r.URL.Path, "/jobs/12/logs"):
				w.Write([]byte("sh: 1: go: not found\n"))
			default:
				t.Errorf("unexpected route %s", r.URL.Path)
				w.WriteHeader(404)
			}
		}))
		c := &Client{site: GitHub, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		adapter := CIProvider{Client: c}
		o, ok, e := adapter.Observe(context.Background(), p, 0)
		if tc.want == "" {
			if !errors.Is(e, distributedci.ErrEvidence) {
				t.Fatalf("success without result accepted: %+v %v", o, e)
			}
		} else if e != nil || !ok || o.State != tc.want {
			t.Fatal(tc.conclusion, o, e)
		}
		s.Close()
	}
}
