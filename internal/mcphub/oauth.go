package mcphub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Probe tells how a server wants to be connected: open (AuthNone, with its
// tool count), OAuth sign-in (MCP authorization: protected resource
// metadata → authorization server → dynamic registration + PKCE), or a
// token pasted by the owner.
type Probe struct {
	Auth  string `json:"auth"`
	Tools int    `json:"tools,omitempty"`
	Error string `json:"error,omitempty"`
	oauth *oauthPlan
}

type oauthPlan struct {
	AuthURL, TokenURL, RegisterURL, Resource string
	Scopes                                   []string
}

// Probe connects without credentials first; only a 401 leads to sign-in.
func (h *Hub) Probe(ctx context.Context, raw string) Probe {
	u, err := ValidURL(raw)
	if err != nil {
		return Probe{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s := Server{ID: "probe", URL: u.String(), Auth: AuthNone}
	if cs, err := h.connect(ctx, s); err == nil {
		n := 0
		for _, err := range cs.Tools(ctx, nil) {
			if err != nil {
				break
			}
			n++
		}
		cs.Close()
		return Probe{Auth: AuthNone, Tools: n}
	}
	c := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpshka-hub","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.Do(req)
	if err != nil {
		return Probe{Error: h.scrubbed(err.Error(), nil)}
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return Probe{Error: fmt.Sprintf("сервер ответил %d — это не MCP-адрес или он недоступен", resp.StatusCode)}
	}
	if plan, err := discover(ctx, c, u, resp.Header.Values("WWW-Authenticate")); err == nil {
		return Probe{Auth: AuthOAuth, oauth: plan}
	}
	return Probe{Auth: AuthToken}
}

func discover(ctx context.Context, c *http.Client, u *url.URL, challenges []string) (*oauthPlan, error) {
	var metaURLs []string
	if cs, err := oauthex.ParseWWWAuthenticate(challenges); err == nil {
		for _, ch := range cs {
			if m := ch.Params["resource_metadata"]; m != "" {
				metaURLs = append(metaURLs, m)
			}
		}
	}
	origin := u.Scheme + "://" + u.Host
	metaURLs = append(metaURLs, origin+"/.well-known/oauth-protected-resource"+strings.TrimSuffix(u.Path, "/"), origin+"/.well-known/oauth-protected-resource")
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	found := false
	for _, m := range metaURLs {
		mu, err := url.Parse(m)
		if err != nil || mu.Scheme != "https" || mu.Host != u.Host {
			continue // metadata of another host could redirect the sign-in
		}
		if getJSON(ctx, c, m, &prm) == nil && len(prm.AuthorizationServers) > 0 {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("no protected resource metadata")
	}
	issuer := prm.AuthorizationServers[0]
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" {
		return nil, errors.New("authorization server is not https")
	}
	path := strings.TrimSuffix(iu.Path, "/")
	for _, m := range []string{iu.Scheme + "://" + iu.Host + "/.well-known/oauth-authorization-server" + path, iu.Scheme + "://" + iu.Host + "/.well-known/openid-configuration" + path, strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"} {
		asm, err := oauthex.GetAuthServerMeta(ctx, m, issuer, c)
		if err == nil && asm != nil && asm.AuthorizationEndpoint != "" && asm.TokenEndpoint != "" {
			resource := prm.Resource
			if resource == "" {
				resource = u.String()
			}
			return &oauthPlan{AuthURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint, RegisterURL: asm.RegistrationEndpoint, Resource: resource, Scopes: prm.ScopesSupported}, nil
		}
	}
	return nil, errors.New("no authorization server metadata")
}

func getJSON(ctx context.Context, c *http.Client, u string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// pending sign-ins: state → flow, 10 minutes.
type flow struct {
	server   string
	verifier string
	plan     oauthPlan
	clientID string
	secret   string
	redirect string
	started  time.Time
}

var (
	flowsMu sync.Mutex
	flows   = map[string]flow{}
)

const flowTTL = 10 * time.Minute

// BeginOAuth registers MCPshka as a client (when the server allows dynamic
// registration) and returns the sign-in URL for the owner's browser.
// redirect is the page's callback on the public MCPshka origin.
func (h *Hub) BeginOAuth(ctx context.Context, id, redirect string) (string, error) {
	s, err := h.Server(id)
	if err != nil {
		return "", err
	}
	p := h.Probe(ctx, s.URL)
	if p.oauth == nil {
		return "", errors.New("сервер не предлагает вход через OAuth — вставьте токен")
	}
	plan := *p.oauth
	c := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	clientID, secret := "", ""
	if plan.RegisterURL != "" {
		reg, err := oauthex.RegisterClient(ctx, plan.RegisterURL, &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{redirect}, ClientName: "MCPshka", GrantTypes: []string{"authorization_code", "refresh_token"},
			ResponseTypes: []string{"code"}, TokenEndpointAuthMethod: "none",
		}, c)
		if err != nil {
			return "", errors.New("сервер отказал в регистрации MCPshka: " + h.scrubbed(err.Error(), nil))
		}
		clientID, secret = reg.ClientID, reg.ClientSecret
	}
	if clientID == "" {
		return "", errors.New("сервер не даёт зарегистрировать MCPshka автоматически — вставьте токен")
	}
	verifier, state := randHex(32), randHex(16)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {plan.Resource}}
	if len(plan.Scopes) > 0 {
		q.Set("scope", strings.Join(plan.Scopes, " "))
	}
	flowsMu.Lock()
	for k, f := range flows {
		if h.Now().Sub(f.started) > flowTTL {
			delete(flows, k)
		}
	}
	flows[state] = flow{server: id, verifier: verifier, plan: plan, clientID: clientID, secret: secret, redirect: redirect, started: h.Now()}
	flowsMu.Unlock()
	sep := "?"
	if strings.Contains(plan.AuthURL, "?") {
		sep = "&"
	}
	return plan.AuthURL + sep + q.Encode(), nil
}

// FinishOAuth exchanges the code the provider sent to the callback; the
// tokens go straight to the server's 0400 secret.
func (h *Hub) FinishOAuth(ctx context.Context, state, code string) (string, error) {
	flowsMu.Lock()
	f, ok := flows[state]
	delete(flows, state)
	flowsMu.Unlock()
	if !ok || h.Now().Sub(f.started) > flowTTL || code == "" {
		return "", errors.New("вход устарел — нажмите «Войти» ещё раз")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {f.redirect},
		"client_id": {f.clientID}, "code_verifier": {f.verifier}, "resource": {f.plan.Resource}}
	if f.secret != "" {
		form.Set("client_secret", f.secret)
	}
	sec, err := h.token(ctx, f.plan.TokenURL, form)
	if err != nil {
		return "", err
	}
	sec.ClientID, sec.ClientSecret, sec.TokenURL, sec.Resource = f.clientID, f.secret, f.plan.TokenURL, f.plan.Resource
	if err := h.PutSecret(f.server, sec); err != nil {
		return "", err
	}
	_, _ = h.Tools(ctx, f.server, true)
	return f.server, nil
}

func (h *Hub) refresh(ctx context.Context, id string, old Secret) (Secret, error) {
	if old.Refresh == "" || old.TokenURL == "" {
		return old, errors.New("no refresh token")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.Refresh}, "client_id": {old.ClientID}}
	if old.ClientSecret != "" {
		form.Set("client_secret", old.ClientSecret)
	}
	if old.Resource != "" {
		form.Set("resource", old.Resource)
	}
	sec, err := h.token(ctx, old.TokenURL, form)
	if err != nil {
		return old, err
	}
	if sec.Refresh == "" {
		sec.Refresh = old.Refresh
	}
	sec.ClientID, sec.ClientSecret, sec.TokenURL, sec.Resource = old.ClientID, old.ClientSecret, old.TokenURL, old.Resource
	return sec, h.PutSecret(id, sec)
}

func (h *Hub) token(ctx context.Context, tokenURL string, form url.Values) (Secret, error) {
	if u, err := url.Parse(tokenURL); err != nil || u.Scheme != "https" {
		return Secret{}, errors.New("token endpoint is not https")
	}
	c := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return Secret{}, errors.New("обмен кода не удался: " + h.scrubbed(err.Error(), nil))
	}
	defer resp.Body.Close()
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&t); err != nil || t.AccessToken == "" {
		return Secret{}, fmt.Errorf("сервер не выдал токен (%d %s)", resp.StatusCode, clip(t.Error, 60))
	}
	sec := Secret{Token: t.AccessToken, Refresh: t.RefreshToken}
	if t.ExpiresIn > 0 {
		sec.Expiry = h.Now().Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
	}
	return sec, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
