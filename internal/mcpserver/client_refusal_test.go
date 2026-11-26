package mcpserver

import (
	"strings"
	"testing"
)

// MCP-047: a client-side refusal gets a type and a next safe action and is
// never attributed to MCPshka without a server receipt.
func TestDiagnoseClientRefusalWithoutReceiptIsExternal(t *testing.T) {
	cases := map[string]string{
		"I can't help with running that tool for safety reasons":        "tool_safety",
		"validating root: unexpected additional properties [\"lease\"]": "schema_mismatch",
		"401 Unauthorized: token expired, please re-authenticate":       "auth",
		"POLICY_DENIED: Face ID approval required":                      "policy",
		"upstream connect error: connection reset by peer (timed out)":  "transport",
		"something odd happened":                                        "unknown",
	}
	for text, want := range cases {
		class, next, receipt := classifyClientRefusal(text)
		if class != want || next == "" {
			t.Fatalf("%q: class %q want %q (next %q)", text, class, want, next)
		}
		if !strings.HasPrefix(receipt, "none_reported") {
			t.Fatalf("%q: no receipt id named, must not be attributed to the server: %q", text, receipt)
		}
	}
	if _, _, receipt := classifyClientRefusal("tool failed, job_id: mvzkk0la"); !strings.HasPrefix(receipt, "reported") {
		t.Fatalf("named receipt not recognised: %q", receipt)
	}
}
