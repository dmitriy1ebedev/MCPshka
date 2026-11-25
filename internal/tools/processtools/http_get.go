package processtools

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"mcpshka/internal/llmgateway"
	"mcpshka/internal/mcphub"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/trust"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
	"unicode/utf8"
)

type HTTPGetInput struct {
	URL   string `json:"url"`
	Limit int    `json:"limit,omitempty"`
}
type HTTPGetResult struct {
	Status    int         `json:"status"`
	Text      string      `json:"text"`
	Truncated bool        `json:"truncated"`
	Trust     trust.Label `json:"trust"`
}

func validateGetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return errors.New("public HTTP(S) URL without credentials or fragment required")
	}
	if !llmgateway.Filter(raw).Allowed {
		return errors.New("URL rejected by secret filter")
	}
	return nil
}
func publicHTTPClient() *http.Client {
	dial := &net.Dialer{Timeout: 3 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil || !mcphub.Public(net.ParseIP(host)) {
			return errors.New("HTTP destination is not public")
		}
		return nil
	}}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: dial.DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second, MaxResponseHeaderBytes: 16 << 10},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("redirect limit reached")
			}
			return validateGetURL(req.URL.String())
		}}
}
func getHTTP(ctx context.Context, client *http.Client, in HTTPGetInput) (HTTPGetResult, error) {
	if err := validateGetURL(in.URL); err != nil {
		return HTTPGetResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return HTTPGetResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return HTTPGetResult{}, errors.New("HTTP request failed or destination refused")
	}
	defer resp.Body.Close()
	limit := in.Limit
	if limit <= 0 {
		limit = 16384
	}
	limit = min(limit, 65536)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return HTTPGetResult{}, errors.New("HTTP body read failed")
	}
	truncated := len(raw) > limit
	if truncated {
		raw = raw[:limit]
	}
	if !utf8.Valid(raw) {
		return HTTPGetResult{}, errors.New("HTTP body must be UTF-8 text")
	}
	v := llmgateway.Filter(string(raw))
	if !v.Allowed {
		return HTTPGetResult{}, errors.New("HTTP body rejected by secret filter")
	}
	return HTTPGetResult{Status: resp.StatusCode, Text: v.Text, Truncated: truncated, Trust: trust.Untrusted}, nil
}
func RegisterHTTPGet(add func(tooldef.Def)) {
	client := publicHTTPClient()
	add(tooldef.New(&mcp.Tool{Name: "http_get", Description: "Fetch public HTTP(S) UTF-8 text without credentials, cookies or proxy. 10 second timeout, 3 redirects, at most 64 KiB. Rejects private/loopback addresses on each connection and secret-like URLs/bodies through the shared egress filter.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in HTTPGetInput) (*mcp.CallToolResult, HTTPGetResult, error) {
		out, err := getHTTP(ctx, client, in)
		return nil, out, err
	}))
}
