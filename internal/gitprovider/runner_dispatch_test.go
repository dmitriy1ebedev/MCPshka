package gitprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDispatchRunnerOnePostPerSite(t *testing.T) {
	tok := "mrt_" + strings.Repeat("a", 64)
	for site, want := range map[Site]string{GitHub: "/repos/dmitriy1ebedev/mcpshka-ci/actions/workflows/runner.yml/dispatches", GitVerse: "/repos/dmitriy_1ebedev/mcpshka-ci/actions/workflows/runner.yml/dispatches", MosHub: "/projects/91385/pipeline"} {
		var got []string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			var v any
			if r.Method != "POST" || json.Unmarshal(b, &v) != nil || !strings.Contains(string(b), tok) {
				t.Errorf("%s: bad request %s %s", site, r.Method, b)
			}
			got = append(got, r.URL.Path)
			w.WriteHeader(204)
		}))
		c := &Client{site: site, base: s.URL, http: s.Client(), token: func(Site) (string, error) { return "fixture", nil }}
		if err := c.DispatchRunner(context.Background(), "https://runner.example", tok); err != nil {
			t.Fatal(site, err)
		}
		s.Close()
		if len(got) != 1 || got[0] != want {
			t.Fatal(site, got)
		}
		if c.DispatchRunner(context.Background(), "http://runner.example", tok) == nil || c.DispatchRunner(context.Background(), "https://runner.example", "x") == nil {
			t.Fatal(site, "invalid credentials accepted")
		}
	}
}
