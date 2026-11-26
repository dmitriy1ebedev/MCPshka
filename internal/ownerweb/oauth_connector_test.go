package ownerweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/authn"
	"mcpshka/internal/statestore"
)

func oauthFixture(t *testing.T) (*OAuthServer, *http.ServeMux, *[]string, *string) {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("static"))
	reg := filepath.Join(t.TempDir(), "auth.json")
	raw, _ := json.Marshal(authn.Registry{Version: 1, Credentials: []authn.Credential{{ID: "owner", TokenSHA256: hex.EncodeToString(sum[:]), Role: "operator", Scopes: []string{"*"}}}})
	if err := os.WriteFile(reg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := authn.New(reg, "")
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := authn.IdentityFromContext(r.Context())
		seen = id.ID + "/" + id.Source
		if r.Host != "localhost" {
			seen += " host " + r.Host
		}
	})
	calls := []string{}
	o := &OAuthServer{db: db, issuer: "https://h.example", identity: "owner", auth: auth, mcp: mcp, now: time.Now,
		broker: func(_ context.Context, args ...string) (json.RawMessage, error) {
			calls = append(calls, args[0])
			if args[0] == "connector-consume" && len(calls) > 2 {
				return nil, errors.New("used")
			}
			return json.RawMessage(`{}`), nil
		}}
	mux := http.NewServeMux()
	o.register(mux)
	return o, mux, &calls, &seen
}

func serve(mux *http.ServeMux, method, target, body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://h.example"+target, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestOAuthConnectorFlow(t *testing.T) {
	isolateConnState(t)
	_, mux, calls, seen := oauthFixture(t)
	if w := serve(mux, "POST", "/oauth/register", `{"redirect_uris":["https://evil.example/cb"]}`, "application/json"); w.Code != http.StatusBadRequest {
		t.Fatal("foreign redirect registered", w.Code)
	}
	w := serve(mux, "POST", "/oauth/register", `{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude"}`, "application/json")
	var client struct {
		ID string `json:"client_id"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &client) != nil || client.ID == "" {
		t.Fatal("register", w.Code, w.Body.String())
	}
	verifier := strings.Repeat("v", 50)
	q := url.Values{"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"}, "state": {"st"}}
	w = serve(mux, "GET", "/oauth/authorize?"+q.Encode(), "", "")
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, "/approval/") || (*calls)[0] != "connector-begin" {
		t.Fatal("authorize", w.Code, loc, *calls)
	}
	id := strings.TrimPrefix(loc, "/approval/")
	w = serve(mux, "GET", "/oauth/approved/"+id, "", "")
	cb, _ := url.Parse(w.Header().Get("Location"))
	code := cb.Query().Get("code")
	if w.Code != http.StatusFound || cb.Host != "claude.ai" || code == "" || cb.Query().Get("state") != "st" {
		t.Fatal("approved", w.Code, cb)
	}
	if w := serve(mux, "GET", "/oauth/approved/"+id, "", ""); w.Code == http.StatusFound {
		t.Fatal("approval replayed")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {client.ID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}}
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("code_verifier", "wrong-"+verifier)
	if w := serve(mux, "POST", "/oauth/token", bad.Encode(), "application/x-www-form-urlencoded"); w.Code != http.StatusBadRequest {
		t.Fatal("PKCE mismatch accepted")
	}
	// The failed attempt consumed the code: single use even when wrong.
	form.Set("code_verifier", verifier)
	if w := serve(mux, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded"); w.Code != http.StatusBadRequest {
		t.Fatal("code reused after a failed exchange")
	}
	if w := serve(mux, "GET", "/mcp", "", ""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatal("anonymous MCP", w.Code)
	}
	_ = seen
}

func TestOAuthTokenReachesMCPAsOwner(t *testing.T) {
	isolateConnState(t)
	o, mux, _, seen := oauthFixture(t)
	c := oauthClient{ID: "cid", RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"}}
	raw, _ := json.Marshal(c)
	_, _ = o.db.PutIfAbsent("oauth/clients/cid", raw)
	g, _ := json.Marshal(oauthGrant{Client: "cid", Redirect: c.RedirectURIs[0], Challenge: pkceChallenge("verifier-verifier-verifier-verifier-verifier"), ExpiresAt: time.Now().Add(time.Minute)})
	_, _ = o.db.PutIfAbsent(tokenKey("oauth/codes/", "the-code"), g)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"the-code"}, "client_id": {"cid"}, "redirect_uri": {c.RedirectURIs[0]}, "code_verifier": {"verifier-verifier-verifier-verifier-verifier"}}
	w := serve(mux, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded")
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &tok) != nil || tok.Access == "" {
		t.Fatal("token", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "https://h.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tok.Access)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if *seen != "owner/oauth" {
		t.Fatal("MCP identity", *seen, rec.Code)
	}
	r = httptest.NewRequest("POST", "https://other.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tok.Access)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatal("foreign host served", rec.Code)
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.Refresh}, "client_id": {"cid"}}
	if w := serve(mux, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded"); w.Code != http.StatusOK {
		t.Fatal("refresh", w.Code)
	}
	if w := serve(mux, "POST", "/oauth/token", refresh.Encode(), "application/x-www-form-urlencoded"); w.Code == http.StatusOK {
		t.Fatal("refresh token reused")
	}
}
