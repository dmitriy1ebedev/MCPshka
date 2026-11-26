package ownerweb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/storagefabric"
)

// Web connect lets the owner authorize a cloud from any browser (phone,
// tablet, computer): storage_connect returns a one-time link on the public
// HTTPS origin, the provider redirects back to /storage/callback, MCPshka
// exchanges the code with PKCE and hands the token to the root vault. rclone
// authorize needs a browser on the same machine (redirect to localhost), which
// an iPad cannot provide.

// storageAppsPath holds the owner's OAuth app registrations (client id and
// secret per provider); the controller reads it, tokens never land here.
var storageAppsPath = "/var/lib/mcpshka/secrets/storage-oauth.json"

const storageConnectTTL = 10 * time.Minute

type StorageOAuthApp struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type storageProviderOAuth struct {
	AuthURL, TokenURL string
	Scope             string
	Extra             url.Values
}

var storageOAuthProviders = map[string]storageProviderOAuth{
	"drive": {AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
		// The whole Drive (owner decision 09.11): drive.file would hide every
		// file MCPshka did not create itself, including the owner's documents.
		Scope: "https://www.googleapis.com/auth/drive", Extra: url.Values{"access_type": {"offline"}, "prompt": {"consent"}}},
	"yandex": {AuthURL: "https://oauth.yandex.ru/authorize", TokenURL: "https://oauth.yandex.ru/token",
		Extra: url.Values{"force_confirm": {"yes"}}},
	"dropbox": {AuthURL: "https://www.dropbox.com/oauth2/authorize", TokenURL: "https://api.dropboxapi.com/oauth2/token",
		Extra: url.Values{"token_access_type": {"offline"}}},
}

var storageAppValue = regexp.MustCompile(`^[A-Za-z0-9._~-]{8,256}$`)

type storageConnectPending struct {
	Provider, Owner, Verifier string
	Expires                   time.Time
}

type storageConnectState struct {
	mu      sync.Mutex
	pending map[string]storageConnectPending
	// http and accept are swapped by tests.
	http   *http.Client
	accept func(ctx context.Context, provider, owner, token string) error
	now    func() time.Time
}

var storageWeb = &storageConnectState{
	pending: map[string]storageConnectPending{},
	http:    &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	accept:  storageVaultAccept,
	now:     time.Now,
}

func storageVaultAccept(ctx context.Context, provider, owner, token string) error {
	raw, err := json.Marshal(storagefabric.Request{Action: "auth_accept", Provider: provider, Owner: owner, Token: token})
	if err != nil || len(raw) > 4096 {
		return errors.New("storage token exceeds budget")
	}
	out, err := brokerclient.Run(ctx, "storage", string(raw))
	if err != nil {
		return err
	}
	var r storagefabric.Result
	if json.Unmarshal(out, &r) != nil {
		return errors.New("invalid vault response")
	}
	return nil
}

func LoadStorageApps() (map[string]StorageOAuthApp, error) {
	apps := map[string]StorageOAuthApp{}
	raw, err := os.ReadFile(storageAppsPath)
	if errors.Is(err, os.ErrNotExist) {
		return apps, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &apps) != nil {
		return nil, errors.New("storage OAuth app file invalid")
	}
	return apps, nil
}

func SaveStorageApp(provider string, app StorageOAuthApp) error {
	if _, ok := storageOAuthProviders[provider]; !ok {
		return errors.New("provider must be drive, yandex or dropbox")
	}
	if !storageAppValue.MatchString(app.ClientID) || !storageAppValue.MatchString(app.ClientSecret) {
		return errors.New("client_id and client_secret must be 8-256 URL-safe characters")
	}
	apps, err := LoadStorageApps()
	if err != nil {
		return err
	}
	apps[provider] = app
	raw, _ := json.MarshalIndent(apps, "", "  ")
	tmp := storageAppsPath + ".tmp"
	if err = os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, storageAppsPath)
}

func storageRedirectURI() (string, error) {
	origin, err := OriginConfigured()
	if err != nil {
		return "", err
	}
	return origin + "/storage/callback", nil
}

func randomURLToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// begin records a one-time pending connect and returns its public link.
func (s *storageConnectState) begin(provider, owner string) (string, time.Time, error) {
	if _, ok := storageOAuthProviders[provider]; !ok {
		return "", time.Time{}, errors.New("provider must be drive, yandex or dropbox")
	}
	origin, err := OriginConfigured()
	if err != nil {
		return "", time.Time{}, err
	}
	state := randomURLToken(24)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.pending {
		if now.After(p.Expires) {
			delete(s.pending, k)
		}
	}
	if len(s.pending) >= 16 {
		return "", time.Time{}, errors.New("too many pending cloud connects; wait for them to expire")
	}
	exp := now.Add(storageConnectTTL)
	s.pending[state] = storageConnectPending{Provider: provider, Owner: owner, Verifier: randomURLToken(48), Expires: exp}
	return origin + "/storage/connect/" + state, exp, nil
}

func (s *storageConnectState) take(state string, consume bool) (storageConnectPending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[state]
	if !ok || s.now().After(p.Expires) {
		delete(s.pending, state)
		return storageConnectPending{}, false
	}
	if consume {
		delete(s.pending, state)
	}
	return p, true
}

func storagePage(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>body{font:18px -apple-system,system-ui,sans-serif;margin:12vh 8vw;color:#222}h1{font-size:24px}</style></head><body><h1>%s</h1><p>%s</p></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
}

// connect redirects the browser to the provider's consent page.
func (s *storageConnectState) connect(w http.ResponseWriter, r *http.Request) {
	state, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/storage/connect/"), "/")
	p, ok := s.take(state, false)
	if !ok || r.Method != http.MethodGet && r.Method != http.MethodPost {
		storagePage(w, http.StatusNotFound, "Ссылка недействительна", "Ссылка устарела или уже использована. Попросите в чате новую: «подключи облако».")
		return
	}
	switch {
	case action == "app" && r.Method == http.MethodPost:
		s.saveApp(w, r, state, p)
		return
	case action == "quick" && r.Method == http.MethodGet:
		s.quickStart(w, state, p)
		return
	case action == "paste" && r.Method == http.MethodPost:
		s.quickPaste(w, r, state, p)
		return
	case action != "" || r.Method != http.MethodGet:
		storagePage(w, http.StatusNotFound, "Ссылка недействительна", "Такой страницы нет. Попросите в чате новую ссылку: «подключи облако».")
		return
	}
	apps, err := LoadStorageApps()
	redirect, rerr := storageRedirectURI()
	app, has := apps[p.Provider]
	if err != nil || rerr != nil {
		storagePage(w, http.StatusServiceUnavailable, "Облако не настроено", "MCPshka не знает свой публичный адрес или не читает настройки облаков.")
		return
	}
	if !has || r.URL.Query().Get("setup") != "" {
		s.setupPage(w, state, p, "")
		return
	}
	cfg := storageOAuthProviders[p.Provider]
	challenge := sha256.Sum256([]byte(p.Verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {app.ClientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	if cfg.Scope != "" {
		q.Set("scope", cfg.Scope)
	}
	for k, v := range cfg.Extra {
		q[k] = v
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, cfg.AuthURL+"?"+q.Encode(), http.StatusFound)
}

// callback exchanges the code and stores the token in the root vault.
func (s *storageConnectState) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, ok := s.take(q.Get("state"), true)
	if !ok || r.Method != http.MethodGet {
		storagePage(w, http.StatusBadRequest, "Ссылка недействительна", "Ссылка устарела или уже использована. Попросите в чате новую.")
		return
	}
	if e := q.Get("error"); e != "" {
		hint := "Можно попробовать ещё раз."
		if p.Provider == "drive" && e == "access_denied" {
			hint = "Если Google написал «приложение не прошло проверку» — в Google Auth Platform → Audience нажмите «Publish app» либо добавьте свой адрес в Test users, и попросите новую ссылку."
		}
		storagePage(w, http.StatusOK, "Доступ не выдан", "Облако вернуло отказ ("+e+"). "+hint)
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 2048 {
		storagePage(w, http.StatusBadRequest, "Нет кода", "Облако не вернуло код авторизации.")
		return
	}
	token, err := s.exchange(r.Context(), p, code)
	if err == nil {
		err = s.accept(r.Context(), p.Provider, p.Owner, token)
	}
	if err != nil {
		storagePage(w, http.StatusBadGateway, "Не получилось", "Код получен, но сохранить доступ не удалось: "+err.Error())
		return
	}
	storagePage(w, http.StatusOK, "Готово", "Облако подключено к MCPshka. Окно можно закрыть и вернуться в чат.")
}

func (s *storageConnectState) exchange(ctx context.Context, p storageConnectPending, code string) (string, error) {
	apps, err := LoadStorageApps()
	if err != nil {
		return "", err
	}
	app, ok := apps[p.Provider]
	redirect, rerr := storageRedirectURI()
	if !ok || rerr != nil {
		return "", errors.New("cloud OAuth app missing")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}, "code_verifier": {p.Verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, storageOAuthProviders[p.Provider].TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", errors.New("provider token endpoint unreachable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	var t struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		Type      string `json:"token_type"`
		ExpiresIn int64  `json:"expires_in"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(body, &t) != nil || resp.StatusCode != http.StatusOK || t.Access == "" {
		reason := t.Error
		if reason == "" {
			reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", errors.New("provider refused the code: " + reason)
	}
	if t.Refresh == "" {
		return "", errors.New("provider issued no refresh token; access would expire within hours")
	}
	if t.Type == "" {
		t.Type = "Bearer"
	}
	out := map[string]any{"access_token": t.Access, "refresh_token": t.Refresh, "token_type": t.Type,
		"client_id": app.ClientID, "client_secret": app.ClientSecret}
	if p.Provider == "drive" {
		out["scope"] = "drive"
	}
	if t.ExpiresIn > 0 {
		out["expiry"] = s.now().Add(time.Duration(t.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	raw, _ := json.Marshal(out)
	return string(raw), nil
}

func registerStorageConnectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/storage/connect/", storageWeb.connect)
	mux.HandleFunc("/storage/callback", storageWeb.callback)
}

// StorageProviderKnown reports a supported storage OAuth provider.
func StorageProviderKnown(provider string) bool { _, ok := storageOAuthProviders[provider]; return ok }

// StorageBegin opens a one-time connect link for owner.
func StorageBegin(provider, owner string) (string, time.Time, error) {
	return storageWeb.begin(provider, owner)
}
