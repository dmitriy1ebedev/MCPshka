package ownerweb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mcpshka/internal/authn"
	"mcpshka/internal/mcptransport"
	"mcpshka/internal/statestore"
)

// OAuth 2.1 for a remote MCP connector (claude.ai cannot send a static
// bearer header). Off unless MCPSHKA_OAUTH_ISSUER is the public HTTPS origin.
// A client registers only callbacks of a switched-on brain (oauth_brains.go:
// Claude, ChatGPT, or one the owner allowed on the page); every
// authorization waits for the owner's Face ID on exactly that request
// (client, redirect, PKCE challenge, state); codes are single use for five
// minutes; the access token acts as MCPSHKA_OAUTH_IDENTITY, so revoking that
// credential also cuts the connector off. Tokens live only as SHA-256.

const (
	oauthCodeTTL    = 5 * time.Minute
	oauthAccessTTL  = 24 * time.Hour
	oauthRefreshTTL = 30 * 24 * time.Hour
	oauthMaxClients = 32
	oauthMaxPending = 16
)

type OAuthServer struct {
	db       *statestore.DB
	issuer   string
	identity string
	auth     *authn.Authenticator
	mcp      http.Handler
	broker   func(ctx context.Context, args ...string) (json.RawMessage, error)
	now      func() time.Time
	// aliases are further https origins (a path is allowed) that reach this
	// server: the paired Lane MCP port and MCPSHKA_OAUTH_ALIASES.
	aliases func() []string
}

type oauthClient struct {
	ID           string    `json:"client_id"`
	RedirectURIs []string  `json:"redirect_uris"`
	Name         string    `json:"client_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type oauthPending struct {
	Client, Redirect, Challenge, State, Binding string
	CreatedAt                                   time.Time
}

type oauthGrant struct {
	Client, Redirect, Challenge string
	ExpiresAt                   time.Time
}

func NewOAuthServer(db *statestore.DB, auth *authn.Authenticator, mcp http.Handler) (*OAuthServer, error) {
	issuer := strings.TrimSuffix(strings.TrimSpace(os.Getenv("MCPSHKA_OAUTH_ISSUER")), "/")
	if issuer == "" {
		return nil, nil
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" {
		return nil, errors.New("MCPSHKA_OAUTH_ISSUER must be a bare https origin")
	}
	identity := strings.TrimSpace(os.Getenv("MCPSHKA_OAUTH_IDENTITY"))
	if identity == "" || db == nil || auth == nil {
		return nil, errors.New("OAuth connector needs MCPSHKA_OAUTH_IDENTITY, the auth registry and SQLite state")
	}
	if _, err := auth.IdentityByID(identity, "oauth"); err != nil {
		return nil, err
	}
	return &OAuthServer{db: db, issuer: issuer, identity: identity, auth: auth, mcp: mcp, broker: portalBroker, now: time.Now, aliases: oauthAliases}, nil
}

func (o *OAuthServer) register(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-protected-resource", o.resourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", o.resourceMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", o.serverMetadata)
	// RFC 8414/9728 path insertion for an issuer with a path (Lane port).
	mux.HandleFunc("/.well-known/oauth-authorization-server/", o.serverMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/", o.resourceMetadata)
	mux.HandleFunc("/oauth/register", o.registerClient)
	mux.HandleFunc("/oauth/authorize", o.authorize)
	mux.HandleFunc("/oauth/approved/", o.approved)
	mux.HandleFunc("/oauth/token", o.token)
	mux.Handle("/mcp", mcptransport.MeasureMCP(http.HandlerFunc(o.bearer), "funnel/claude"))
}

// oauthAliases reads the extra origins: the Lane port's public URL (through
// Lane ChatGPT/Claude met a foreign issuer and failed OAuth) and the comma or
// space separated MCPSHKA_OAUTH_ALIASES.
func oauthAliases() []string {
	out := strings.FieldsFunc(os.Getenv("MCPSHKA_OAUTH_ALIASES"), func(r rune) bool { return r == ',' || r == ' ' })
	if u := connWeb.lane().PublicURL(); u != "" {
		out = append(out, u)
	}
	return out
}

// issuerFor picks the issuer the client used: the configured origin, or the
// one alias on the request host (a path hint from a path-inserted well-known
// URL or X-Forwarded-Prefix disambiguates). Unknown hosts are refused.
func (o *OAuthServer) issuerFor(r *http.Request, pathHint string) (string, bool) {
	if u, _ := url.Parse(o.issuer); r.Host == u.Host {
		return o.issuer, true
	}
	if o.aliases == nil {
		return "", false
	}
	hint := strings.TrimSuffix(pathHint, "/")
	if hint == "" {
		hint = strings.TrimSuffix(r.Header.Get("X-Forwarded-Prefix"), "/")
	}
	var match []string
	for _, a := range o.aliases() {
		a = strings.TrimSuffix(strings.TrimSpace(a), "/")
		u, err := url.Parse(a)
		if err != nil || u.Scheme != "https" || u.Host != r.Host || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		if hint == "" || u.Path == hint || u.Path+"/mcp" == hint {
			match = append(match, a)
		}
	}
	if len(match) != 1 {
		return "", false
	}
	return match[0], true
}

func (o *OAuthServer) host(w http.ResponseWriter, r *http.Request) bool {
	_, ok := o.hostIssuer(w, r)
	return ok
}

func (o *OAuthServer) hostIssuer(w http.ResponseWriter, r *http.Request) (string, bool) {
	hint := ""
	for _, p := range []string{"/.well-known/oauth-authorization-server/", "/.well-known/oauth-protected-resource/"} {
		if rest, ok := strings.CutPrefix(r.URL.Path, p); ok {
			hint = "/" + rest
		}
	}
	if hint == "/mcp" {
		hint = ""
	}
	issuer, ok := o.issuerFor(r, hint)
	if !ok {
		http.Error(w, "invalid host", http.StatusForbidden)
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return issuer, true
}

func oauthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	oauthJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenKey(prefix, token string) string {
	sum := sha256.Sum256([]byte(token))
	return prefix + hex.EncodeToString(sum[:])
}

func (o *OAuthServer) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	issuer, ok := o.hostIssuer(w, r)
	if !ok {
		return
	}
	oauthJSON(w, http.StatusOK, map[string]any{"resource": issuer + "/mcp", "authorization_servers": []string{issuer}, "bearer_methods_supported": []string{"header"}})
}

func (o *OAuthServer) serverMetadata(w http.ResponseWriter, r *http.Request) {
	issuer, ok := o.hostIssuer(w, r)
	if !ok {
		return
	}
	oauthJSON(w, http.StatusOK, map[string]any{
		"issuer": issuer, "authorization_endpoint": issuer + "/oauth/authorize", "token_endpoint": issuer + "/oauth/token",
		"registration_endpoint": issuer + "/oauth/register", "response_types_supported": []string{"code"},
		"grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (o *OAuthServer) registerClient(w http.ResponseWriter, r *http.Request) {
	if !o.host(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		Name         string   `json:"client_name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil || len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 4 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !redirectAllowed(u) {
			noteRejected(u, strings.ToValidUTF8(in.Name[:min(len(in.Name), 64)], ""), o.now().UTC())
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "this AI client is not allowed yet: the owner can allow it on the MCPshka Connections page")
			return
		}
	}
	id, err := randomToken(18)
	if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "server_error", "secure random unavailable")
		return
	}
	c := oauthClient{ID: id, RedirectURIs: in.RedirectURIs, Name: strings.ToValidUTF8(in.Name[:min(len(in.Name), 64)], ""), CreatedAt: o.now().UTC()}
	raw, _ := json.Marshal(c)
	if ok, err := o.db.PutIfAbsentBounded("oauth/clients/"+id, raw, "oauth/clients/", oauthMaxClients); err != nil || !ok {
		oauthError(w, http.StatusServiceUnavailable, "server_error", "client registry full")
		return
	}
	oauthJSON(w, http.StatusCreated, map[string]any{"client_id": id, "redirect_uris": c.RedirectURIs, "client_name": c.Name,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
}

func (o *OAuthServer) client(id string) (oauthClient, bool) {
	var c oauthClient
	if len(id) == 0 || len(id) > 64 {
		return c, false
	}
	raw, err := o.db.Get("oauth/clients/" + id)
	return c, err == nil && json.Unmarshal(raw, &c) == nil && c.ID == id
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (o *OAuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	if !o.host(w, r) {
		return
	}
	q := r.URL.Query()
	c, ok := o.client(q.Get("client_id"))
	redirect := q.Get("redirect_uri")
	valid := false
	for _, u := range c.RedirectURIs {
		valid = valid || u == redirect
	}
	if !ok || !valid || !redirectAllowed(redirect) {
		http.Error(w, "unknown client or redirect, or this AI client is switched off", http.StatusBadRequest)
		return
	}
	challenge, state := q.Get("code_challenge"), q.Get("state")
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(challenge) < 43 || len(challenge) > 128 || len(state) > 512 {
		http.Error(w, "code flow with S256 PKCE required", http.StatusBadRequest)
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "secure random unavailable", http.StatusServiceUnavailable)
		return
	}
	id := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(strings.Join([]string{c.ID, redirect, challenge, state}, "\x00")))
	p := oauthPending{Client: c.ID, Redirect: redirect, Challenge: challenge, State: state, Binding: hex.EncodeToString(sum[:]), CreatedAt: o.now().UTC()}
	encoded, _ := json.Marshal(p)
	o.sweep("oauth/pending/", func(raw []byte) bool {
		var old oauthPending
		return json.Unmarshal(raw, &old) != nil || o.now().Sub(old.CreatedAt) > oauthCodeTTL
	})
	if ok, err := o.db.PutIfAbsentBounded("oauth/pending/"+id, encoded, "oauth/pending/", oauthMaxPending); err != nil || !ok {
		http.Error(w, "too many pending authorizations", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if _, err := o.broker(ctx, "connector-begin", id, p.Binding); err != nil {
		http.Error(w, "Face ID unavailable", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/approval/"+id, http.StatusFound)
}

func (o *OAuthServer) approved(w http.ResponseWriter, r *http.Request) {
	if !o.host(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/oauth/approved/")
	if _, err := hex.DecodeString(id); err != nil || len(id) != 32 {
		http.NotFound(w, r)
		return
	}
	raw, err := o.db.Get("oauth/pending/" + id)
	var p oauthPending
	if err != nil || json.Unmarshal(raw, &p) != nil || o.now().Sub(p.CreatedAt) > 2*oauthCodeTTL {
		http.Error(w, "authorization expired; start again in Claude", http.StatusGone)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if _, err := o.broker(ctx, "connector-consume", id, p.Binding); err != nil {
		http.Error(w, "Face ID not confirmed for this authorization", http.StatusForbidden)
		return
	}
	_, _ = o.db.Delete("oauth/pending/"+id, raw)
	code, err := randomToken(32)
	if err != nil {
		http.Error(w, "secure random unavailable", http.StatusServiceUnavailable)
		return
	}
	g, _ := json.Marshal(oauthGrant{Client: p.Client, Redirect: p.Redirect, Challenge: p.Challenge, ExpiresAt: o.now().Add(oauthCodeTTL)})
	if ok, err := o.db.PutIfAbsent(tokenKey("oauth/codes/", code), g); err != nil || !ok {
		http.Error(w, "code not stored", http.StatusServiceUnavailable)
		return
	}
	target, _ := url.Parse(p.Redirect)
	v := target.Query()
	v.Set("code", code)
	if p.State != "" {
		v.Set("state", p.State)
	}
	target.RawQuery = v.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// take reads and deletes a single-use record; a concurrent second use fails.
func (o *OAuthServer) take(key string, out *oauthGrant) bool {
	raw, err := o.db.Get(key)
	if err != nil || json.Unmarshal(raw, out) != nil {
		return false
	}
	ok, err := o.db.Delete(key, raw)
	return err == nil && ok && o.now().Before(out.ExpiresAt)
}

func (o *OAuthServer) token(w http.ResponseWriter, r *http.Request) {
	if !o.host(w, r) {
		return
	}
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "form POST required")
		return
	}
	f := r.PostForm
	client := f.Get("client_id")
	var g oauthGrant
	switch f.Get("grant_type") {
	case "authorization_code":
		if !o.take(tokenKey("oauth/codes/", f.Get("code")), &g) || g.Client != client || g.Redirect != f.Get("redirect_uri") || pkceChallenge(f.Get("code_verifier")) != g.Challenge {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code invalid, used, expired or PKCE mismatch")
			return
		}
	case "refresh_token":
		if !o.take(tokenKey("oauth/refresh/", f.Get("refresh_token")), &g) || g.Client != client {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token invalid or expired")
			return
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "authorization_code or refresh_token")
		return
	}
	if c, ok := o.client(client); !ok || !clientAllowed(c) {
		oauthError(w, http.StatusBadRequest, "invalid_client", "client was removed or its AI client is switched off")
		return
	}
	access, err1 := randomToken(32)
	refresh, err2 := randomToken(32)
	if err1 != nil || err2 != nil {
		oauthError(w, http.StatusServiceUnavailable, "server_error", "secure random unavailable")
		return
	}
	now := o.now()
	a, _ := json.Marshal(oauthGrant{Client: client, ExpiresAt: now.Add(oauthAccessTTL)})
	rf, _ := json.Marshal(oauthGrant{Client: client, ExpiresAt: now.Add(oauthRefreshTTL)})
	o.sweep("oauth/tokens/", expiredGrant(now))
	o.sweep("oauth/refresh/", expiredGrant(now))
	if err := o.db.Put(tokenKey("oauth/tokens/", access), a); err != nil {
		oauthError(w, http.StatusServiceUnavailable, "server_error", "token not stored")
		return
	}
	if err := o.db.Put(tokenKey("oauth/refresh/", refresh), rf); err != nil {
		oauthError(w, http.StatusServiceUnavailable, "server_error", "token not stored")
		return
	}
	oauthJSON(w, http.StatusOK, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(oauthAccessTTL.Seconds()), "refresh_token": refresh})
}

func expiredGrant(now time.Time) func([]byte) bool {
	return func(raw []byte) bool {
		var g oauthGrant
		return json.Unmarshal(raw, &g) != nil || !now.Before(g.ExpiresAt)
	}
}

// sweep drops dead records so the bounded prefixes never fill with garbage.
func (o *OAuthServer) sweep(prefix string, dead func([]byte) bool) {
	keys, err := o.db.List(prefix)
	if err != nil {
		return
	}
	for _, k := range keys {
		if raw, err := o.db.Get(k); err == nil && dead(raw) {
			_, _ = o.db.Delete(k, raw)
		}
	}
}

func (o *OAuthServer) bearer(w http.ResponseWriter, r *http.Request) {
	issuer, ok := o.hostIssuer(w, r)
	if !ok {
		return
	}
	challenge := `Bearer resource_metadata="` + issuer + `/.well-known/oauth-protected-resource"`
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var g oauthGrant
	raw, err := o.db.Get(tokenKey("oauth/tokens/", strings.TrimSpace(token)))
	if !ok || err != nil || json.Unmarshal(raw, &g) != nil || !o.now().Before(g.ExpiresAt) {
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if c, ok := o.client(g.Client); !ok || !clientAllowed(c) {
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "connector removed or switched off", http.StatusUnauthorized)
		return
	}
	identity, err := o.auth.IdentityByID(o.identity, "oauth")
	if err != nil {
		http.Error(w, "connector identity revoked", http.StatusForbidden)
		return
	}
	// The public Host was matched to the issuer above. The MCP handler's own
	// DNS-rebinding guard refuses any non-loopback Host on a loopback socket,
	// and the portal always listens on loopback behind the HTTPS proxy.
	r = r.Clone(authn.WithIdentity(r.Context(), identity))
	r.Host = "localhost"
	o.mcp.ServeHTTP(w, r)
}
