package ownerweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCP225CaddyTemplateHasMatchingLANFaceIDRoutes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "tunnel", "mcpshka-faceid.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	caddy := string(raw)
	for _, want := range []string{
		"http://:8798 {",
		"bind 172.17.0.1",
		"h96-host.example.ts.net {",
		"bind 192.168.77.10",
		"reverse_proxy 127.0.0.1:8799",
		"header_up Host h96-host.example.ts.net",
		"header_up X-Forwarded-Proto https",
	} {
		if !strings.Contains(caddy, want) {
			t.Fatalf("face ID template missing %q", want)
		}
	}
	var routes []string
	for _, line := range strings.Split(caddy, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "@allowed path ") {
			routes = append(routes, trimmed)
		}
	}
	if len(routes) != 2 || routes[0] != routes[1] {
		t.Fatalf("LAN and tunnel OAuth/approval paths diverged: %v", routes)
	}
	if !strings.Contains(routes[0], "/oauth/*") ||
		!strings.Contains(routes[0], "/approval/*") ||
		!strings.Contains(routes[0], "/.well-known/oauth-authorization-server") {
		t.Fatal("Face ID/OAuth route is incomplete")
	}
}

func TestValidApprovalPrefix(t *testing.T) {
	for v, want := range map[string]bool{"latest": true, "8dc1037d": true, "8dc1037d5523ce84834a858347b6284": true, "8dc1037": false, "8DC1037D": false, "8dc1037d5523ce84834a858347b6284b": false, "../x": false, "": false} {
		if got := ValidApprovalPrefix(v); got != want {
			t.Fatalf("%q: got %v want %v", v, got, want)
		}
	}
}

// A lost /finish response may mean the approval was committed by the broker.
// Reconciliation must read durable state, never replay the WebAuthn assertion.
func TestPasskeyPortalRecoverAuthorizedWithoutReplayingFinish(t *testing.T) {
	for _, part := range []string{
		"let finishSent=false;",
		"const recoverAuthorization=async()=>",
		"const data=await request('/approval/'+id+'/options');",
		"data.state!=='authorized'&&data.state!=='active'",
		"if(finishSent){",
		"if(await recoverAuthorization())return true;",
		"if(await reconcileAfterFinish())return;",
		"Не повторяйте операцию",
	} {
		if !strings.Contains(PasskeyHTML, part) {
			t.Fatalf("passkey recovery lacks %q", part)
		}
	}
	parts := strings.Split(PasskeyHTML, "const recoverAuthorization=async()=>")
	if len(parts) != 2 {
		t.Fatal("recovery helper not unique")
	}
	recovery := strings.Split(parts[1], "window.addEventListener('pageshow'")[0]
	if strings.Contains(recovery, "request('/approval/'+id+'/finish'") ||
		strings.Contains(recovery, "navigator.credentials.get") {
		t.Fatal("reconciliation must be GET-only and require no new biometric challenge")
	}
}
