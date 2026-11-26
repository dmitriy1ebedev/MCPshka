package mcptransport

import "net/http"

// OriginHeader marks every response MCPshka itself produced (MCP-050). A
// 502/503/504 seen by a client without this header was generated upstream
// (tunnel or provider) and is EXTERNAL; with it, the failure is LOCAL and
// its body carries MCPshka's own classified error.
const OriginHeader = "X-MCPshka-Origin"

func MarkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(OriginHeader, "local")
		next.ServeHTTP(w, r)
	})
}

// ErrorDomain classifies an HTTP failure as seen by a client or the tunnel.
func ErrorDomain(status int, h http.Header) string {
	switch {
	case status < 400:
		return ""
	case h.Get(OriginHeader) == "local":
		return "LOCAL"
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return "EXTERNAL_TUNNEL"
	}
	return "EXTERNAL"
}
