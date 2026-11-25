package ownerweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 0.7.0: a client that reaches MCPshka through the Lane MCP port sees an
// issuer on that host (with its path), not a foreign one; unknown hosts stay
// refused.
func TestOAuthIssuerFollowsRequestHost(t *testing.T) {
	o := &OAuthServer{issuer: "https://h.example", aliases: func() []string {
		return []string{"https://h96.tail.ts.net/lane-mcp/ab12/", "https://alt.example"}
	}}
	meta := func(host, path string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		if strings.Contains(path, "protected-resource") {
			o.resourceMetadata(rec, req)
		} else {
			o.serverMetadata(rec, req)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := meta("h.example", "/.well-known/oauth-authorization-server"); code != 200 || out["issuer"] != "https://h.example" {
		t.Fatalf("main issuer %d %v", code, out)
	}
	if code, out := meta("h96.tail.ts.net", "/.well-known/oauth-authorization-server"); code != 200 || out["issuer"] != "https://h96.tail.ts.net/lane-mcp/ab12" || out["token_endpoint"] != "https://h96.tail.ts.net/lane-mcp/ab12/oauth/token" {
		t.Fatalf("lane issuer %d %v", code, out)
	}
	if code, out := meta("h96.tail.ts.net", "/.well-known/oauth-authorization-server/lane-mcp/ab12"); code != 200 || out["issuer"] != "https://h96.tail.ts.net/lane-mcp/ab12" {
		t.Fatalf("path-inserted metadata %d %v", code, out)
	}
	if code, out := meta("h96.tail.ts.net", "/.well-known/oauth-protected-resource/lane-mcp/ab12/mcp"); code != 200 || out["resource"] != "https://h96.tail.ts.net/lane-mcp/ab12/mcp" {
		t.Fatalf("resource metadata %d %v", code, out)
	}
	if code, _ := meta("h96.tail.ts.net", "/.well-known/oauth-authorization-server/other"); code != http.StatusForbidden {
		t.Fatal("foreign path on alias host accepted")
	}
	if code, _ := meta("evil.example", "/.well-known/oauth-authorization-server"); code != http.StatusForbidden {
		t.Fatal("unknown host accepted")
	}
	if code, out := meta("alt.example", "/.well-known/oauth-protected-resource"); code != 200 || out["resource"] != "https://alt.example/mcp" {
		t.Fatalf("env alias %d %v", code, out)
	}
}
