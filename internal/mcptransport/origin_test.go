package mcptransport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// MCP-050: a local error is distinguishable from a tunnel 503 by origin.
func TestOriginSeparatesLocalFromTunnelErrors(t *testing.T) {
	h := MarkOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if d := ErrorDomain(rec.Code, rec.Header()); d != "LOCAL" {
		t.Fatalf("local 503 classified %s", d)
	}
	if d := ErrorDomain(http.StatusGatewayTimeout, http.Header{}); d != "EXTERNAL_TUNNEL" {
		t.Fatalf("tunnel 504 classified %s", d)
	}
	if d := ErrorDomain(200, http.Header{}); d != "" {
		t.Fatal(d)
	}
}
