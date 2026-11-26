package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFreeCIMinutesFallsBackToOwnRunTiming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/settings/billing/"):
			http.Error(w, "forbidden", http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			if !strings.HasPrefix(r.URL.Query().Get("created"), ">=") {
				t.Errorf("month filter missing: %q", r.URL.RawQuery)
			}
			// A busy month has more pages: the estimate must still work.
			w.Header().Set("Link", `<https://api.github.com/x?page=2>; rel="next"`)
			w.Write([]byte(`{"total_count":3,"workflow_runs":[{"id":1},{"id":2}]}`))
		case strings.HasSuffix(r.URL.Path, "/runs/1/timing"):
			w.Write([]byte(`{"billable":{"UBUNTU":{"total_ms":600001}}}`))
		case strings.HasSuffix(r.URL.Path, "/runs/2/timing"):
			http.Error(w, "gone", http.StatusNotFound)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := &Client{site: GitHub, base: srv.URL, http: srv.Client(), token: func(Site) (string, error) { return "fixture", nil }}
	remaining, e := c.freeCIMinutes(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	// run1 11 min + run2 unreadable 15 + one unseen 15 = 41; ×1.25 + 300 = 351.25 → 2000-352
	if remaining != 1648 {
		t.Fatalf("remaining = %d, want 1648", remaining)
	}
}
