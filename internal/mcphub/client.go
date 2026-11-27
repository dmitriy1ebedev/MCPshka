package mcphub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is one cached tool of a server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	ReadOnly    bool            `json:"read_only,omitempty"`
}

type toolCache struct {
	Fetched time.Time `json:"fetched"`
	Tools   []Tool    `json:"tools"`
}

const (
	toolsTTL    = 6 * time.Hour
	callTimeout = 90 * time.Second
	maxResult   = 256 << 10
)

// dialer refuses non-public targets (guard.go).
var dialer = &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}

// transport is shared; a variable so tests can reach httptest servers.
var transport http.RoundTripper = &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second, MaxIdleConns: 8, IdleConnTimeout: 60 * time.Second, ForceAttemptHTTP2: true}

// bearer adds the server's own token; no other server's credential is ever
// attached (one transport per call, built from this server's secret only).
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func (h *Hub) httpClient(ctx context.Context, s Server) (*http.Client, error) {
	c := &http.Client{Transport: transport, Timeout: callTimeout}
	if s.Auth == AuthNone {
		return c, nil
	}
	sec, ok := h.secret(s.ID)
	if !ok {
		return nil, errors.New("LOGIN_NEEDED: войдите в «" + s.Title + "» на странице «Плагины»")
	}
	if s.Auth == AuthOAuth && !sec.Expiry.IsZero() && h.Now().After(sec.Expiry.Add(-time.Minute)) {
		fresh, err := h.refresh(ctx, s.ID, sec)
		if err != nil {
			return nil, errors.New("LOGIN_NEEDED: вход в «" + s.Title + "» истёк, войдите снова")
		}
		sec = fresh
	}
	c.Transport = bearer{token: sec.Token, base: transport}
	return c, nil
}

func (h *Hub) connect(ctx context.Context, s Server) (*mcp.ClientSession, error) {
	hc, err := h.httpClient(ctx, s)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpshka-hub", Version: "1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
}

// Tools returns the server's tools, from cache unless older than toolsTTL
// or refresh is set.
func (h *Hub) Tools(ctx context.Context, id string, refresh bool) ([]Tool, error) {
	if !refresh {
		if c, ok := h.cachedTools(id); ok && h.Now().Sub(c.Fetched) < toolsTTL {
			return c.Tools, nil
		}
	}
	s, err := h.Server(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	cs, err := h.connect(ctx, s)
	if err != nil {
		if c, ok := h.cachedTools(id); ok {
			return c.Tools, nil // stale beats nothing; the page shows the error
		}
		return nil, h.clean(err)
	}
	defer cs.Close()
	var out []Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, h.clean(err)
		}
		schema, _ := json.Marshal(t.InputSchema)
		ro := t.Annotations != nil && t.Annotations.ReadOnlyHint
		out = append(out, Tool{Name: t.Name, Description: clip(t.Description, 1024), InputSchema: schema, ReadOnly: ro})
		if len(out) >= 500 {
			break
		}
	}
	raw, _ := json.Marshal(toolCache{Fetched: h.Now().UTC(), Tools: out})
	_ = writeAtomic(h.path("tools", id+".json"), raw, 0o600)
	return out, nil
}

func (h *Hub) cachedTools(id string) (toolCache, bool) {
	raw, err := os.ReadFile(h.path("tools", id+".json"))
	if err != nil {
		return toolCache{}, false
	}
	var c toolCache
	return c, json.Unmarshal(raw, &c) == nil
}

// CallInput is one tool call through the hub.
type CallInput struct {
	Server, Tool string
	Args         map[string]any
	Caller       string
	// Approved: the caller holds the owner's Face ID window; required for
	// any tool the server does not mark read-only.
	Approved bool
	// Known are host secrets the guard must also refuse (set by the server).
	Known []string
}

// CallResult is the redacted outcome.
type CallResult struct {
	Server  string `json:"server"`
	Tool    string `json:"tool"`
	Status  string `json:"status"`
	Text    string `json:"text,omitempty"`
	Data    any    `json:"structured,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Trust   string `json:"trust"`
	Ms      int64  `json:"ms"`
}

// Call runs one tool on a connected server; every outcome lands in the ledger.
func (h *Hub) Call(ctx context.Context, in CallInput) (CallResult, error) {
	start := h.Now()
	res := CallResult{Server: in.Server, Tool: in.Tool, Trust: "UNTRUSTED"}
	entry := Call{At: start.UTC(), Server: in.Server, Tool: clip(in.Tool, 128), Caller: clip(in.Caller, 40), Args: Redact(in.Args)}
	fail := func(status string, err error) (CallResult, error) {
		res.Status = status
		entry.Status, entry.Error = status, clip(h.scrubbed(err.Error(), in.Known), 300)
		entry.Millis = h.Now().Sub(start).Milliseconds()
		h.record(entry)
		return res, errors.New(entry.Error)
	}
	s, err := h.Server(in.Server)
	if err != nil {
		return fail("ERROR", err)
	}
	if s.Disabled {
		return fail("ERROR", errors.New("«"+s.Title+"» отключён на странице «Плагины»"))
	}
	tools, err := h.Tools(ctx, s.ID, false)
	if err != nil {
		return fail("ERROR", err)
	}
	var tool *Tool
	for i := range tools {
		if tools[i].Name == in.Tool {
			tool = &tools[i]
		}
	}
	if tool == nil {
		return fail("ERROR", fmt.Errorf("у «%s» нет инструмента %q", s.Title, in.Tool))
	}
	if !tool.ReadOnly && !in.Approved {
		return fail("APPROVAL_REQUIRED", errors.New("инструмент может менять данные у «"+s.Title+"»: нужен admin_session_id (окно Face ID)"))
	}
	if err := Guard(in.Args, append(h.secrets(), in.Known...)); err != nil {
		return fail("SECRET_BLOCKED", err)
	}
	out, _ := json.Marshal(in.Args)
	entry.BytesOut = len(out)
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		return fail("TIMEOUT", ctx.Err())
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	cs, err := h.connect(ctx, s)
	if err != nil {
		return fail(timeoutOr(ctx, "ERROR"), h.clean(err))
	}
	defer cs.Close()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: in.Tool, Arguments: in.Args})
	if err != nil {
		return fail(timeoutOr(ctx, "ERROR"), h.clean(err))
	}
	var b strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
			b.WriteByte('\n')
		}
		if b.Len() > maxResult {
			break
		}
	}
	known := append(h.secrets(), in.Known...)
	res.Text = clip(scrub(strings.TrimSpace(b.String()), known), maxResult)
	res.Data, res.IsError = r.StructuredContent, r.IsError
	raw, _ := json.Marshal(r)
	entry.BytesIn = len(raw)
	res.Status, entry.Status = "OK", "OK"
	if r.IsError {
		res.Status, entry.Status = "TOOL_ERROR", "TOOL_ERROR"
		entry.Error = clip(scrub(res.Text, known), 300)
	}
	res.Ms = h.Now().Sub(start).Milliseconds()
	entry.Millis = res.Ms
	h.record(entry)
	return res, nil
}

func timeoutOr(ctx context.Context, status string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	return status
}

// clean strips secrets from an error before it is shown or logged.
func (h *Hub) clean(err error) error { return errors.New(h.scrubbed(err.Error(), nil)) }

func (h *Hub) scrubbed(text string, extra []string) string {
	return scrub(text, append(h.secrets(), extra...))
}
