package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// MCP-076: HTTP 200 or a present credential never means CONNECTED; each of
// the six signals is observed separately and UNKNOWN stays UNKNOWN.
func TestProviderHTTP200IsNotConnected(t *testing.T) {
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := httpProbe(srv.URL, "Authorization", token)(context.Background())
	if s.NetworkReachable.State != "yes" || s.Configured.State != "yes" || s.Authenticated.State != "yes" || s.APIVerified.State != "yes" {
		t.Fatalf("200 with token: %+v", s)
	}
	if s.WorkflowReady.State != "unknown" || readiness(s) == "CONNECTED" || readiness(s) != "API_VERIFIED" {
		t.Fatalf("a probe must never claim CONNECTED: %s %+v", readiness(s), s.WorkflowReady)
	}
	anon := httpProbe(srv.URL, "", "")(context.Background())
	if anon.Authenticated.State != "unknown" || anon.Authorized.State != "unknown" {
		t.Fatalf("anonymous 200 proves no login: %+v", anon)
	}
	code = http.StatusUnauthorized
	if s := httpProbe(srv.URL, "Authorization", token)(context.Background()); s.Authenticated.State != "no" || readiness(s) != "BLOCKED" {
		t.Fatalf("401: %+v", s)
	}
	code = http.StatusForbidden
	if s := httpProbe(srv.URL, "Authorization", token)(context.Background()); s.Authorized.State != "no" || s.Authenticated.State != "unknown" {
		t.Fatalf("403: %+v", s)
	}
	if s := httpProbe(srv.URL, "Authorization", filepath.Join(t.TempDir(), "missing"))(context.Background()); s.Configured.State != "no" {
		t.Fatalf("missing token: %+v", s.Configured)
	}
	all := snapshot(context.Background(), map[string]probe{})
	for _, st := range all {
		if st.Identity.ID != st.Name || st.Identity.Quota.State != "UNKNOWN" || st.Quota.State != "UNKNOWN" || st.Readiness == "" || st.Signals.NetworkReachable.ObservedAt.IsZero() || (st.Name != "h96") != st.Optional {
			t.Fatalf("unprobed site must be explicit UNKNOWN and optional: %+v", st)
		}
	}
	if len(Unsupported) < 2 {
		t.Fatal("gitlab and icloud must be listed as unsupported")
	}
}
