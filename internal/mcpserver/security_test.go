package mcpserver

import (
	"context"
	"mcpshka/internal/ownerweb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasskeyPortalFailClosed(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "")
	w := httptest.NewRecorder()
	ownerweb.PasskeyPortal(w, httptest.NewRequest(http.MethodGet, "http://localhost/passkey/enroll", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled: %d", w.Code)
	}
}

func TestPasskeyPortalOriginAndNonce(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "https://approve.example.test")
	w := httptest.NewRecorder()
	ownerweb.PasskeyPortal(w, httptest.NewRequest(http.MethodGet, "http://bad.example/passkey/enroll", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unexpected host: %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "https://approve.example.test/passkey/enroll/begin", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://bad.example")
	w = httptest.NewRecorder()
	ownerweb.PasskeyPortal(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "https://approve.example.test/passkey/enroll", nil)
	w = httptest.NewRecorder()
	ownerweb.PasskeyPortal(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Face ID") {
		t.Fatalf("page not served: %d", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") || strings.Contains(csp, "script-src 'unsafe-inline'") {
		t.Fatalf("invalid CSP %q", csp)
	}
	if !strings.Contains(w.Body.String(), "<script nonce=") {
		t.Fatal("missing script nonce")
	}
	if w.Header().Get("Cache-Control") != "no-store, max-age=0" {
		t.Fatal("missing no-store")
	}
}

func TestPasskeyOnlyHandlerExcludesControlPlane(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "https://confirm.example.test")
	for _, path := range []string{"/mcp", "/artifacts/secret", "/healthz"} {
		w := httptest.NewRecorder()
		ownerweb.PasskeyOnlyHandler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://confirm.example.test"+path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("public handler leaked %s: %d", path, w.Code)
		}
	}
}
func TestPasskeyListenerRejectsUnsafeOrMissingOrigin(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "")
	l, e := ownerweb.OpenPasskeyListener()
	if e != nil || l != nil {
		t.Fatalf("disabled listener: %v %v", l, e)
	}
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "http://confirm.example.test")
	if _, e = ownerweb.OpenPasskeyListener(); e == nil {
		t.Fatal("accepted plaintext origin")
	}
}
func TestRunHTTPRequiresTokenBeforeStarting(t *testing.T) {
	err := RunHTTP(context.Background(), "127.0.0.1:0", "")
	if err == nil || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}
