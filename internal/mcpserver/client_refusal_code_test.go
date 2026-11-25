package mcpserver

import "testing"

// MCP-039: typed client refusal codes; dispatch is proven only by a receipt.
func TestClientRefusalCodesAndDispatch(t *testing.T) {
	cases := []struct{ text, code, dispatched string }{
		{"The tool call was blocked for safety reasons", "CLIENT_TOOL_SAFETY_BLOCK", "not_observed"},
		{"POLICY_DENIED: unit not allowlisted (job id mw01z9zvm)", "MCP_POLICY_DENIED", "receipt_reported"},
		{"needs Face ID: admin_session_begin", "FACE_ID_REQUIRED", "not_observed"},
		{"lane-mcp@vps: Permission denied (publickey)", "SSH_AUTH_FAILED", "not_observed"},
		{"unknown tool approved_task_prepare", "CLIENT_SCHEMA_STALE", "not_observed"},
		{"connection reset by peer", "NETWORK_DOWN", "not_observed"},
	}
	for _, c := range cases {
		class, _, receipt := classifyClientRefusal(c.text)
		code, d := clientRefusalCode(class, c.text, receipt)
		if code != c.code || d != c.dispatched {
			t.Errorf("%q: class %s -> %s/%s, want %s/%s", c.text, class, code, d, c.code, c.dispatched)
		}
	}
}
