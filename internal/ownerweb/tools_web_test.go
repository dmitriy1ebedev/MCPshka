package ownerweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedCall struct {
	name string
	args map[string]any
}

// fakeTools records ToolCall invocations; the returned func snapshots them
// under the same lock (logout releases the lease from another goroutine).
func fakeTools(t *testing.T) func() []recordedCall {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	old := ToolCall
	ToolCall = func(_ context.Context, name string, args map[string]any) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, recordedCall{name, args})
		if name == "project_lease_acquire" {
			return json.RawMessage(`{"status":{"project_root":"/root/repos/demo","held":true},"token":"secret-token"}`), nil
		}
		return json.RawMessage(`{"ok":true}`), nil
	}
	t.Cleanup(func() { ToolCall = old })
	return func() []recordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedCall(nil), calls...)
	}
}

func post(t *testing.T, p *connPortal, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	if c == nil {
		p.route(rr, connReq(http.MethodPost, path, body))
	} else {
		p.route(rr, connReq(http.MethodPost, path, body, c))
	}
	return rr
}

// The owner pages reach only WebTools; the lease token lives server-side in
// the Face ID session and is injected only for the project it was taken on.
func TestOwnerToolsProxyScopesAndLease(t *testing.T) {
	p, _ := newConnTestPortal(t)
	calls := fakeTools(t)
	if rr := post(t, p, "/connections/api/tool", `{"tool":"fs_read","args":{"path":"/x"}}`, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("tool without session: %d", rr.Code)
	}
	session := connLoginFlow(t, p)
	for _, name := range []string{"exec_start", "release_apply", "project_lease_release", "tool_invoke"} {
		if rr := post(t, p, "/connections/api/tool", `{"tool":"`+name+`","args":{}}`, session); rr.Code != http.StatusBadRequest {
			t.Fatalf("%s reachable from the web: %d", name, rr.Code)
		}
	}
	if len(calls()) != 0 {
		t.Fatalf("refused tools reached ToolCall: %+v", calls())
	}
	if rr := post(t, p, "/connections/api/code-open", `{"path":"/root/repos/demo"}`, session); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "secret-token") {
		t.Fatalf("code-open: %d %s", rr.Code, rr.Body)
	}
	post(t, p, "/connections/api/tool", `{"tool":"fs_write","args":{"path":"/root/repos/demo/a.go","content_base64":"eA==","lease_token":"forged"}}`, session)
	post(t, p, "/connections/api/tool", `{"tool":"fs_write","args":{"path":"/root/repos/other/a.go","content_base64":"eA=="}}`, session)
	post(t, p, "/connections/api/tool", `{"tool":"fs_read","args":{"path":"/root/repos/demo/a.go"}}`, session)
	got := calls()
	if len(got) != 4 || got[1].args["lease_token"] != "secret-token" {
		t.Fatalf("lease not injected for own project: %+v", got)
	}
	if _, ok := got[2].args["lease_token"]; ok {
		t.Fatal("lease leaked to another project")
	}
	if _, ok := got[3].args["lease_token"]; ok {
		t.Fatal("lease attached to a read")
	}
	// Logout releases the lease.
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/logout", "{}", session))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range calls() {
			if c.name == "project_lease_release" && c.args["token"] == "secret-token" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("logout did not release the «Код» lease")
}

func TestOwnerPagesServeWithNonce(t *testing.T) {
	p, _ := newConnTestPortal(t)
	for _, path := range []string{"/connections/resources", "/connections/code", "/connections/ide", "/connections/containers", "/connections/runners"} {
		rr := httptest.NewRecorder()
		p.route(rr, connReq(http.MethodGet, path, ""))
		csp := rr.Header().Get("Content-Security-Policy")
		i := strings.Index(csp, "nonce-")
		if rr.Code != http.StatusOK || i < 0 || !strings.Contains(rr.Body.String(), `<script nonce="`+csp[i+6:i+38]+`">`) {
			t.Fatalf("%s: %d csp=%q", path, rr.Code, csp)
		}
		if strings.Contains(rr.Body.String(), "{{LAN}}") {
			t.Fatalf("%s: placeholder left in page", path)
		}
	}
}

func TestIDECallbacks(t *testing.T) {
	newConnTestPortal(t) // hermetic brain state
	for uri, want := range map[string]bool{
		"http://127.0.0.1:33418/":                      true,
		"http://localhost:6274/oauth/callback":         true,
		"cursor://anysphere.cursor-mcp/oauth/callback": true,
		"https://vscode.dev/redirect":                  false, // listed in URIs, not matched here
		"http://127.0.0.1/":                            false, // loopback needs a port
		"http://evil.example:8080/cb":                  false,
		"http://127.0.0.1:1/cb?code=x":                 false,
		"http://user@127.0.0.1:1/":                     false,
		"cursor://evil/oauth":                          false,
		"javascript://127.0.0.1:1/%0aalert(1)":         false,
	} {
		if got := ideCallback(uri); got != want {
			t.Errorf("ideCallback(%q) = %v, want %v", uri, got, want)
		}
	}
	if !redirectAllowed("http://127.0.0.1:33418/") || !redirectAllowed("https://vscode.dev/redirect") {
		t.Fatal("IDE brain on by default but callbacks refused")
	}
}
