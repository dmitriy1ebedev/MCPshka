package brain

import (
	"encoding/json"
	"fmt"
)

// noise is what MCPshka answers carry for clients and auditors but a model
// deciding the next call does not need. 0.19.0 acceptance: tool_resolve
// answers (~3 candidates with readiness and output schemas) were cut
// mid-JSON at 6 KB and GigaChat answered from the broken half.
var noise = map[string]bool{
	"call_via": true, "constraints": true, "backend_healthy": true, "client_exposed": true,
	"provider_authenticated": true, "server_capability": true, "permission_granted": true,
	"why": true, "confidence": true, "revision": true, "output_schema": true, "trust": true,
	"mutation": true, "annotations": true, "estimated_tool_calls": true,
}

// compact shrinks a JSON result under budget while keeping it valid JSON:
// noise keys go first, then long strings and long lists are shortened.
// Anything that is not JSON, or already fits, is returned unchanged.
func compact(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	var v any
	if json.Unmarshal([]byte(text), &v) != nil {
		return text
	}
	v = prune(v, 0, 0)
	for _, limits := range [][2]int{{600, 12}, {300, 8}, {160, 5}, {80, 3}} {
		out := prune(v, limits[0], limits[1])
		if raw, err := json.Marshal(out); err == nil && len(raw) <= budget {
			return string(raw)
		}
	}
	raw, _ := json.Marshal(prune(v, 80, 3))
	return string(raw)
}

// prune drops noise keys; with str > 0 it also shortens strings to str
// bytes and lists to list items (a marker item says how many were left out).
func prune(v any, str, list int) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if !noise[k] {
				out[k] = prune(val, str, list)
			}
		}
		return out
	case []any:
		n := len(x)
		if list > 0 && n > list {
			x = x[:list]
		}
		out := make([]any, 0, len(x)+1)
		for _, val := range x {
			out = append(out, prune(val, str, list))
		}
		if len(out) < n {
			out = append(out, fmt.Sprintf("…ещё %d", n-len(out)))
		}
		return out
	case string:
		if str > 0 && len(x) > str {
			return clip(x, str)
		}
	}
	return v
}
