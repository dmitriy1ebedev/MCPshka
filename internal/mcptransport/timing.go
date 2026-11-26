package mcptransport

import (
	"mcpshka/internal/toolstats"
	"net/http"
	"strings"
)

// MeasureMCP records only fixed route labels and durations, never URLs,
// headers, arguments or response bodies. A route hint is telemetry, not auth.
func MeasureMCP(next http.Handler, route string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label := route
		if label == "" {
			label = "local"
			// The tunnel preserves the upstream UA; clients may pin the route in
			// their connection URL when a proxy replaces it.
			if strings.Contains(strings.ToLower(r.UserAgent()), "openai") {
				label = "openai-tunnel"
			}
			switch r.URL.Query().Get("route") {
			case "openai-tunnel":
				label = "openai-tunnel"
			case "local":
				label = "local"
			}
		}
		ctx, finish := toolstats.BeginHTTP(r.Context())
		defer finish(label)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
