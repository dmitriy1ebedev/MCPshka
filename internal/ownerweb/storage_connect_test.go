package ownerweb

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storageConnectFixture(t *testing.T, refresh bool) (*storageConnectState, *map[string]string, *http.ServeMux) {
	t.Helper()
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "https://mcpshka.example")
	old := storageAppsPath
	storageAppsPath = filepath.Join(t.TempDir(), "apps.json")
	t.Cleanup(func() { storageAppsPath = old })
	if err := SaveStorageApp("drive", StorageOAuthApp{ClientID: "client-123.apps", ClientSecret: "secret-456"}); err != nil {
		t.Fatal(err)
	}
	var seen url.Values
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		seen = r.PostForm
		out := map[string]any{"access_token": "ACCESS", "token_type": "Bearer", "expires_in": 3599}
		if refresh {
			out["refresh_token"] = "REFRESH"
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(tokens.Close)
	saved := storageOAuthProviders["drive"]
	cfg := saved
	cfg.TokenURL = tokens.URL
	storageOAuthProviders["drive"] = cfg
	t.Cleanup(func() { storageOAuthProviders["drive"] = saved })
	accepted := map[string]string{}
	s := &storageConnectState{pending: map[string]storageConnectPending{}, http: tokens.Client(), now: time.Now,
		accept: func(_ context.Context, provider, owner, token string) error {
			accepted["provider"], accepted["owner"], accepted["token"] = provider, owner, token
			if seen.Get("code_verifier") == "" || seen.Get("client_secret") != "secret-456" || seen.Get("redirect_uri") != "https://mcpshka.example/storage/callback" {
				t.Fatalf("token request incomplete: %v", seen)
			}
			return nil
		}}
	mux := http.NewServeMux()
	mux.HandleFunc("/storage/connect/", s.connect)
	mux.HandleFunc("/storage/callback", s.callback)
	return s, &accepted, mux
}

func TestStorageConnectFullFlow(t *testing.T) {
	s, accepted, mux := storageConnectFixture(t, true)
	link, _, err := s.begin("drive", "owner-a")
	if err != nil || !strings.HasPrefix(link, "https://mcpshka.example/storage/connect/") {
		t.Fatalf("%q %v", link, err)
	}
	state := strings.TrimPrefix(link, "https://mcpshka.example/storage/connect/")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/storage/connect/"+state, nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if rec.Code != http.StatusFound || loc.Host != "accounts.google.com" || q.Get("state") != state || q.Get("access_type") != "offline" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("bad redirect %d %s", rec.Code, loc)
	}
	p, _ := s.take(state, false)
	sum := sha256.Sum256([]byte(p.Verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("PKCE challenge does not match the verifier")
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/storage/callback?state="+state+"&code=CODE", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Готово") {
		t.Fatalf("callback %d %s", rec.Code, rec.Body.String())
	}
	var tok map[string]any
	json.Unmarshal([]byte((*accepted)["token"]), &tok)
	if (*accepted)["owner"] != "owner-a" || tok["refresh_token"] != "REFRESH" || tok["client_id"] != "client-123.apps" || tok["client_secret"] != "secret-456" || tok["expiry"] == nil || tok["scope"] != "drive" || q.Get("scope") != "https://www.googleapis.com/auth/drive" {
		t.Fatalf("vault got %v", *accepted)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/storage/callback?state="+state+"&code=CODE", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal("a used link was accepted twice")
	}
}

func TestStorageConnectRefusesExpiredAndNoRefresh(t *testing.T) {
	s, accepted, mux := storageConnectFixture(t, false)
	link, _, _ := s.begin("drive", "owner-a")
	state := strings.TrimPrefix(link, "https://mcpshka.example/storage/connect/")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/storage/callback?state="+state+"&code=CODE", nil))
	if rec.Code != http.StatusBadGateway || (*accepted)["token"] != "" {
		t.Fatalf("token without refresh stored: %d %v", rec.Code, *accepted)
	}
	link, _, _ = s.begin("drive", "owner-a")
	state = strings.TrimPrefix(link, "https://mcpshka.example/storage/connect/")
	s.now = func() time.Time { return time.Now().Add(storageConnectTTL + time.Minute) }
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/storage/connect/"+state, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal("expired link still redirects")
	}
	if _, _, err := s.begin("icloud", "owner-a"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

// A cloud without an OAuth app gets the wizard instead of a refusal: the
// page shows the redirect address, takes the keys in the browser and goes on
// to consent; the quick rclone way is offered only where it exists.
func TestStorageConnectWizardTakesAppInBrowser(t *testing.T) {
	s, _, mux := storageConnectFixture(t, true)
	link, _, err := s.begin("yandex", "owner-a")
	if err != nil {
		t.Fatalf("a link must be issued without an app: %v", err)
	}
	path := strings.TrimPrefix(link, "https://mcpshka.example")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	page := rec.Body.String()
	// Yandex answers 410 to rclone's own app since 11.10.2026: no quick way.
	if rec.Code != http.StatusOK || !strings.Contains(page, "https://mcpshka.example/storage/callback") || strings.Contains(page, path+"/quick") || !strings.Contains(page, "410") || !strings.Contains(page, path+"/app") {
		t.Fatalf("wizard page %d %s", rec.Code, page)
	}
	link, _, _ = s.begin("dropbox", "owner-a")
	dpath := strings.TrimPrefix(link, "https://mcpshka.example")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", dpath, nil))
	if !strings.Contains(rec.Body.String(), dpath+"/quick") {
		t.Fatalf("dropbox keeps the quick way: %s", rec.Body.String())
	}
	post := func(target, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec = post(path+"/app", "client_id=x&client_secret=y"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Ключи не приняты") {
		t.Fatalf("short keys accepted: %d", rec.Code)
	}
	if rec = post(path+"/app", "client_id=yandex-client-1&client_secret=yandex-secret-1"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
		t.Fatalf("app save: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if loc, _ := url.Parse(rec.Header().Get("Location")); rec.Code != http.StatusFound || loc.Host != "oauth.yandex.ru" || loc.Query().Get("client_id") != "yandex-client-1" {
		t.Fatalf("consent redirect after app save: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Google has no quick way: rclone's shared client is being retired.
	link, _, _ = s.begin("drive", "owner-a")
	path = strings.TrimPrefix(link, "https://mcpshka.example")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path+"?setup=1", nil))
	if page = rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(page, "/quick") || !strings.Contains(page, "Publish app") {
		t.Fatalf("drive wizard %d %s", rec.Code, page)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path+"/quick", nil))
	if !strings.Contains(rec.Body.String(), "быстрого способа нет") {
		t.Fatal("drive quick way must be refused")
	}
	if rec = post(path+"/paste", "url=nonsense"); !strings.Contains(rec.Body.String(), "не тот адрес") {
		t.Fatalf("bad paste: %s", rec.Body.String())
	}
}
