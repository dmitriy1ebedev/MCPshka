package mcptransport

import (
	"mcpshka/internal/toolstats"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRouteMeasurements(t *testing.T) {
	for _, tc := range []struct{ route, query, ua, want string }{
		{"", "", "", "local"}, {"", "", "OpenAI MCP", "openai-tunnel"},
		{"", "?route=openai-tunnel", "", "openai-tunnel"},
		{"funnel/claude", "?route=local", "OpenAI MCP", "funnel/claude"},
	} {
		before := map[string]int{}
		for _, v := range toolstats.Default.HTTPReport() {
			before[v.Route] = v.Requests
		}
		h := MeasureMCP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			finish := toolstats.BeginHTTPTool(r.Context())
			defer finish()
			w.WriteHeader(204)
		}), tc.route)
		r := httptest.NewRequest("POST", "/mcp"+tc.query, nil)
		r.Header.Set("User-Agent", tc.ua)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		for _, v := range toolstats.Default.HTTPReport() {
			want := before[v.Route]
			if v.Route == tc.want {
				want++
			}
			if v.Requests != want {
				t.Fatalf("%+v route %s: %+v", tc, v.Route, v)
			}
		}
		if w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
}
