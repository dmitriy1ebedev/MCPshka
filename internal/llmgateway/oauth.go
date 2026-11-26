package llmgateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OAuth is a client-credentials exchange: the key goes as Basic
// authorization to TokenURL with Scope, the reply's access_token is the
// bearer until expires_at. GigaChat: token_url
// https://ngw.devices.sberbank.ru:9443/api/v2/oauth, scope GIGACHAT_API_PERS,
// request_id_header RqUID, ca_file — the Russian Trusted Root CA bundle.
type OAuth struct {
	TokenURL        string `json:"token_url"`
	Scope           string `json:"scope,omitempty"`
	RequestIDHeader string `json:"request_id_header,omitempty"`
	// CAFile adds a PEM root to the system pool for this endpoint and its
	// token URL (it never replaces the system roots).
	CAFile string `json:"ca_file,omitempty"`
}

type token struct {
	bearer  string
	expires time.Time
}

func (o *OAuth) validate() error {
	u, err := url.Parse(o.TokenURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: oauth.token_url must be https://host/...", ErrUnavailable)
	}
	if strings.ContainsAny(o.Scope, " \r\n") || len(o.Scope) > 64 {
		return fmt.Errorf("%w: oauth.scope invalid", ErrUnavailable)
	}
	if h := o.RequestIDHeader; h != "" && (len(h) > 32 || strings.ContainsAny(h, " :\r\n")) {
		return fmt.Errorf("%w: oauth.request_id_header invalid", ErrUnavailable)
	}
	return nil
}

// client is base with the endpoint's extra CA, never following redirects.
func (c Config) client(base *http.Client) (*http.Client, error) {
	if base == nil {
		base = &http.Client{}
	}
	bounded := *base
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.OAuth == nil || c.OAuth.CAFile == "" {
		return &bounded, nil
	}
	pem, err := os.ReadFile(c.OAuth.CAFile)
	if err != nil {
		return nil, fmt.Errorf("%w: oauth.ca_file unreadable", ErrUnavailable)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w: oauth.ca_file has no PEM certificate", ErrUnavailable)
	}
	tr, _ := http.DefaultTransport.(*http.Transport)
	if t, ok := bounded.Transport.(*http.Transport); ok {
		tr = t
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	bounded.Transport = tr
	return &bounded, nil
}

// bearer is the key, or for an OAuth endpoint a cached access token.
func (g *Gateway) bearer(ctx context.Context, c Config, key string) (string, error) {
	if c.OAuth == nil {
		return key, nil
	}
	if key == "" {
		return "", fmt.Errorf("%w: oauth endpoint needs key_file", ErrUnavailable)
	}
	sum := sha256.Sum256([]byte(c.OAuth.TokenURL + "\n" + c.OAuth.Scope + "\n" + key))
	id := hex.EncodeToString(sum[:])
	g.mu.Lock()
	t, ok := g.tokens[id]
	g.mu.Unlock()
	if ok && time.Until(t.expires) > time.Minute {
		return t.bearer, nil
	}
	form := url.Values{}
	if c.OAuth.Scope != "" {
		form.Set("scope", c.OAuth.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.OAuth.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("LLM oauth invalid request")
	}
	req.Header.Set("Authorization", "Basic "+key)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mcpshka-llmgateway")
	if h := c.OAuth.RequestIDHeader; h != "" {
		req.Header.Set(h, requestID())
	}
	client, err := c.client(g.HTTP)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("LLM oauth request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM oauth status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"` // unix ms (GigaChat)
		ExpiresIn   int64  `json:"expires_in"` // seconds (RFC 6749)
	}
	if json.Unmarshal(raw, &out) != nil || out.AccessToken == "" {
		return "", errors.New("LLM oauth reply has no access_token")
	}
	t = token{bearer: out.AccessToken, expires: time.Now().Add(25 * time.Minute)}
	switch {
	case out.ExpiresAt > 0:
		t.expires = time.UnixMilli(out.ExpiresAt)
	case out.ExpiresIn > 0:
		t.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	g.mu.Lock()
	if g.tokens == nil {
		g.tokens = map[string]token{}
	}
	g.tokens[id] = t
	g.mu.Unlock()
	return t.bearer, nil
}

func requestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
