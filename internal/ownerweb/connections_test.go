package ownerweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const connTestOrigin = "https://h96.example"

func newConnTestPortal(t *testing.T) (*connPortal, *[]string) {
	t.Helper()
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", connTestOrigin)
	old := connStateDir
	connStateDir = t.TempDir()
	t.Cleanup(func() { connStateDir = old })
	oldLane := laneBase
	laneBase = "http://127.0.0.1:1" // hermetic: never the real Lane on H96
	t.Cleanup(func() { laneBase = oldLane })
	var mu sync.Mutex
	var calls []string
	p := &connPortal{
		sessions: map[string]connSession{}, logins: map[string]connLogin{}, checks: map[string]ConnCheck{},
		http: http.DefaultClient, now: time.Now,
		broker: func(_ context.Context, family string, args ...string) (json.RawMessage, error) {
			mu.Lock()
			calls = append(calls, family+" "+strings.Join(args, " "))
			mu.Unlock()
			switch {
			case family == "passkey" && args[0] == "connections-begin":
				if len(args[1]) != 32 || len(args[2]) != 64 {
					return nil, errors.New("bad grant identity")
				}
				return json.RawMessage(`{"state":"PENDING","options":{"publicKey":{"challenge":"AA"}}}`), nil
			case family == "passkey" && args[0] == "connections-consume":
				return json.RawMessage(`{"state":"CONSUMED"}`), nil
			case family == "storage":
				return json.RawMessage(`{"state":"observed","providers":[{"id":"drive","state":"credential_present","authenticated":"unverified"}]}`), nil
			}
			return nil, errors.New("unexpected broker call")
		},
	}
	return p, &calls
}

func connReq(method, path, body string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(method, connTestOrigin+path, strings.NewReader(body))
	r.Host = "h96.example"
	if method == http.MethodPost {
		r.Header.Set("Origin", connTestOrigin)
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func cookieNamed(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func connLoginFlow(t *testing.T, p *connPortal) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/login", "{}"))
	if rr.Code != http.StatusOK {
		t.Fatalf("login %d %s", rr.Code, rr.Body)
	}
	pending := cookieNamed(rr, connLoginCookie)
	if pending == nil || !pending.HttpOnly || !pending.Secure || pending.SameSite != http.SameSiteStrictMode || pending.Path != "/connections" {
		t.Fatalf("login cookie not locked down: %+v", pending)
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/session", "{}", pending))
	if rr.Code != http.StatusOK {
		t.Fatalf("session %d %s", rr.Code, rr.Body)
	}
	s := cookieNamed(rr, connCookie)
	if s == nil || !s.HttpOnly || !s.Secure {
		t.Fatalf("session cookie missing: %+v", s)
	}
	// The pending grant is single use.
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/session", "{}", pending))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("replayed grant accepted: %d", rr.Code)
	}
	return s
}

func TestConnectionsAPINeedsFaceIDSession(t *testing.T) {
	p, calls := newConnTestPortal(t)
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/api/catalog", ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("catalog without session: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/token", `{"id":"github","token":"ghp_abcdefghijkl"}`, &http.Cookie{Name: connCookie, Value: strings.Repeat("a", 64)}))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("forged session accepted: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	r := connReq(http.MethodPost, "/connections/login", "{}")
	r.Header.Set("Origin", "https://evil.example")
	p.route(rr, r)
	if rr.Code != http.StatusForbidden || len(*calls) != 0 {
		t.Fatalf("cross-origin login reached the broker: %d %v", rr.Code, *calls)
	}
}

func TestConnectionsTokenIsCheckedBeforeWriteAndNeverReturned(t *testing.T) {
	p, _ := newConnTestPortal(t)
	session := connLoginFlow(t, p)
	accept := true
	saved := connCatalog
	t.Cleanup(func() { connCatalog = saved })
	connCatalog = append([]connProvider{{ID: "fake", Group: "Код и CI", Title: "Fake", Kind: "token", Path: "fake/api_token",
		check: func(_ context.Context, _ *http.Client, token string) ConnCheck {
			if !accept {
				return ConnCheck{Detail: "провайдер отклонил токен (HTTP 401)"}
			}
			return ConnCheck{OK: true, Account: "owner"}
		}}}, saved...)

	accept = false
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/token", `{"id":"fake","token":"secret-token-0001"}`, session))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected token: %d %s", rr.Code, rr.Body)
	}
	if _, err := os.Stat(filepath.Join(connStateDir, "fake/api_token")); !os.IsNotExist(err) {
		t.Fatal("a rejected token was written")
	}

	accept = true
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/token", `{"id":"fake","token":"secret-token-0002"}`, session))
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "secret-token") {
		t.Fatalf("save: %d %s", rr.Code, rr.Body)
	}
	st, err := os.Stat(filepath.Join(connStateDir, "fake/api_token"))
	if err != nil || st.Mode().Perm() != 0o400 {
		t.Fatalf("token file: %v %v", st, err)
	}
	raw, _ := os.ReadFile(filepath.Join(connStateDir, "fake/api_token"))
	if string(raw) != "secret-token-0002" {
		t.Fatalf("stored %q", raw)
	}
	// Replacing a read-only token still works (atomic rename).
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/token", `{"id":"fake","token":"secret-token-0003"}`, session))
	if raw, _ := os.ReadFile(filepath.Join(connStateDir, "fake/api_token")); rr.Code != http.StatusOK || string(raw) != "secret-token-0003" {
		t.Fatalf("replace: %d %q", rr.Code, raw)
	}

	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/api/catalog", "", session))
	body := rr.Body.String()
	if rr.Code != http.StatusOK || strings.Contains(body, "secret-token") || !strings.Contains(body, `"last4":"…0003"`) || !strings.Contains(body, `"state":"connected"`) {
		t.Fatalf("catalog: %d %s", rr.Code, body)
	}
	var cat struct{ Connections []ConnStatus }
	_ = json.Unmarshal(rr.Body.Bytes(), &cat)
	for _, c := range cat.Connections {
		if c.ID == "drive" && c.State != "connected" {
			t.Fatalf("drive state %q", c.State)
		}
	}
}

func TestConnectionsRejectsBadInput(t *testing.T) {
	p, _ := newConnTestPortal(t)
	session := connLoginFlow(t, p)
	for _, body := range []string{`{"id":"nope","token":"abcdefghijk"}`, `{"id":"github","token":"short"}`, `{"id":"github","token":"has space inside"}`, `{"id":"drive","token":"abcdefghijk"}`, `not json`} {
		rr := httptest.NewRecorder()
		p.route(rr, connReq(http.MethodPost, "/connections/api/token", body, session))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/token", `{"id":"github","token":"`+strings.Repeat("a", connMaxBody)+`"}`, session))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", rr.Code)
	}
}

func TestConnectionsPageHasNonceCSP(t *testing.T) {
	p, _ := newConnTestPortal(t)
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections", ""))
	csp := rr.Header().Get("Content-Security-Policy")
	if rr.Code != http.StatusOK || !strings.Contains(csp, "script-src 'nonce-") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("page: %d %q", rr.Code, csp)
	}
	nonce := strings.TrimSuffix(strings.SplitN(strings.SplitN(csp, "'nonce-", 2)[1], "'", 2)[0], "'")
	if !strings.Contains(rr.Body.String(), `<script nonce="`+nonce+`">`) || strings.Contains(rr.Body.String(), "innerHTML") {
		t.Fatal("script not bound to the nonce or uses innerHTML")
	}
}

func TestConnectionsCatalogIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range connCatalog {
		if seen[c.ID] || c.Title == "" || c.Group == "" || c.Why == "" {
			t.Fatalf("bad row %+v", c.ID)
		}
		seen[c.ID] = true
		switch c.Kind {
		case "token":
			if c.Path == "" || c.check == nil || filepath.IsAbs(c.Path) || strings.Contains(c.Path, "..") {
				t.Fatalf("token row %s", c.ID)
			}
		case "cloud":
			if _, ok := cloudBackends[c.ID]; !ok {
				t.Fatalf("cloud row %s has no rclone backend", c.ID)
			}
		case "form":
			if _, ok := connForms[c.ID]; !ok {
				t.Fatalf("form row %s has no fields", c.ID)
			}
		case "agent", "lane":
		default:
			t.Fatalf("kind %s", c.Kind)
		}
	}
	if len(connGroups()) < 6 {
		t.Fatalf("groups %v", connGroups())
	}
}
