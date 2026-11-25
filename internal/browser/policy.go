package browser

import (
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Private HTTP origins are configured by the operator, never by a run spec.
// Literal IPs prevent a DNS name from changing the meaning of an allowlist entry.
func allowedHTTP(endpoint string) bool {
	host, port, err := net.SplitHostPort(endpoint)
	ip, e := netip.ParseAddr(host)
	number, portErr := strconv.Atoi(port)
	if err != nil || e != nil || (!ip.IsPrivate() && !ip.IsLoopback()) || portErr != nil || number < 1 || number > 65535 {
		return false
	}
	for _, entry := range strings.Split(os.Getenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST"), ",") {
		if strings.TrimSpace(entry) == endpoint {
			return true
		}
	}
	return false
}

func requestAllowed(raw, site string, document bool) bool {
	if raw == "about:blank" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if document {
		return OnSite(raw, site)
	}
	// Cross-site HTTPS resources are needed by public sites; plain HTTP resources
	// are restricted to the run's exact configured local origin.
	return u.Scheme == "https" && u.Host != "" || u.Scheme == "http" && u.Host == site && allowedHTTP(u.Host)
}

func failureStage(detail string) string {
	if strings.Contains(detail, "ERR_CERT_") || strings.Contains(detail, "ERR_SSL_") {
		return "tls"
	}
	return "navigation"
}

func authURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasPrefix(u.Path, "/auth/authorize") || strings.HasPrefix(u.Path, "/auth/login")
}

// CDP decisions are sent without waiting inside the read loop. Chromium holds
// each request until this check, including server redirects and script navigation.
func (b *cdp) routeRequest(raw json.RawMessage) {
	var r struct {
		RequestID    string `json:"requestId"`
		ResourceType string `json:"resourceType"`
		Request      struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	method := "Fetch.continueRequest"
	params := map[string]any{"requestId": r.RequestID}
	document := r.ResourceType == "Document" && !(b.free && strings.HasPrefix(r.Request.URL, "https://"))
	if !requestAllowed(r.Request.URL, b.site, document) {
		method = "Fetch.failRequest"
		params["errorReason"] = "BlockedByClient"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	msg, _ := json.Marshal(map[string]any{"id": b.next, "sessionId": b.session, "method": method, "params": params})
	_, _ = b.w.Write(append(msg, 0))
}
func (b *cdp) observeResponse(raw json.RawMessage) {
	var r struct {
		Type     string `json:"type"`
		Response struct {
			Status int `json:"status"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &r) == nil && r.Type == "Document" && (r.Response.Status == 401 || r.Response.Status == 403) {
		b.mu.Lock()
		b.authRequired = true
		b.mu.Unlock()
	}
}
