package gitprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpshka/internal/distributedci"
)

func TestSourceCraftReportSourceBinding(t *testing.T) {
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), []string{"mcpshka/a", "mcpshka/b", "mcpshka/c", "mcpshka/d"}, nil, nil, 4)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []bool{false, true} {
		o := distributedci.Observation{Provider: "sourcecraft", Shard: 3, Commit: p.Commit, Tree: p.Tree, Snapshot: p.Snapshot, PlanHash: p.Hash(), Packages: p.Shards[3], State: "PASS", RunID: 1}
		if bad {
			o.Tree = p.Commit
		}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fixture" {
				t.Error("missing fixed origin auth")
			}
			if r.URL.Path == "/report" {
				json.NewEncoder(w).Encode(o)
				return
			}
			w.Write([]byte(`{"runs":[{"status":"success","workflows":[{"tasks":[{"cubes":[{"artifacts":[{"local_path":"report.json","status":"success","download_url":"/report"}]}]}]}]}]}`))
		}))
		c := &Client{site: SourceCraft, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		adapter := CIProvider{Client: c}
		got, found, e := adapter.Observe(context.Background(), p, 3)
		if bad && e == nil || !bad && (e != nil || !found || got.State != "PASS") {
			t.Fatal(got, found, e)
		}
		if _, e = adapter.sourceCraftArtifact(context.Background(), "https://evil.invalid/report"); e == nil {
			t.Fatal("untrusted artifact URL")
		}
		s.Close()
	}
}

func TestBuildArtifactDownloadRejectsForeignRedirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.invalid/steal")
		w.WriteHeader(302)
	}))
	defer s.Close()
	c := &Client{site: GitHub, base: s.URL, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, token: func(Site) (string, error) { return "fixture", nil }}
	if _, e := c.downloadCI(context.Background(), "/artifact"); e == nil {
		t.Fatal("credential destination not checked")
	}
}
