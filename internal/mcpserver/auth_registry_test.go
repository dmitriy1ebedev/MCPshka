package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/authn"
	"mcpshka/internal/mcptransport"
)

func writeHTTPAuthRegistry(t *testing.T, path string, reg authn.Registry) {
	t.Helper()
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBearerAuthRegistryIdentityRotationAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	tokenA := "registry-token-a"
	tokenB := "registry-token-b"
	writeHTTPAuthRegistry(t, path, authn.Registry{
		Version:     1,
		Credentials: []authn.Credential{{ID: "chatgpt-a", TokenSHA256: authn.HashToken(tokenA)}},
	})
	authenticator, err := authn.New(path, "legacy-must-not-bypass")
	if err != nil {
		t.Fatal(err)
	}
	handler := mcptransport.BearerAuth(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := authn.IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, "missing identity", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Test-Identity", identity.ID)
		w.WriteHeader(http.StatusNoContent)
	}))

	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://example.invalid/mcp", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if got := call(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", got.Code)
	}
	first := call(tokenA)
	if first.Code != http.StatusNoContent || first.Header().Get("X-Test-Identity") != "chatgpt-a" {
		t.Fatalf("token A response=%d identity=%q", first.Code, first.Header().Get("X-Test-Identity"))
	}
	if got := call("legacy-must-not-bypass"); got.Code != http.StatusUnauthorized {
		t.Fatalf("legacy fallback bypass status=%d", got.Code)
	}

	writeHTTPAuthRegistry(t, path, authn.Registry{
		Version: 1,
		Credentials: []authn.Credential{
			{ID: "chatgpt-a", TokenSHA256: authn.HashToken(tokenA), Revoked: true},
			{ID: "chatgpt-b", TokenSHA256: authn.HashToken(tokenB)},
		},
	})
	if got := call(tokenA); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d", got.Code)
	}
	second := call(tokenB)
	if second.Code != http.StatusNoContent || second.Header().Get("X-Test-Identity") != "chatgpt-b" {
		t.Fatalf("token B response=%d identity=%q", second.Code, second.Header().Get("X-Test-Identity"))
	}
}
