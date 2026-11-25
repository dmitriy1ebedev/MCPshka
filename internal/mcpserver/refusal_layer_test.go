package mcpserver

import (
	"strings"
	"testing"
)

func TestClientSafetyReportIsNotServerLeasePolicy(t *testing.T) {
	report := "ChatGPT safety blocked lease acquisition before dispatch; no MCP response"
	class, next, receipt := classifyClientRefusal(report)
	code, dispatch := clientRefusalCode(class, report, receipt)
	if class != "tool_safety" || code != "CLIENT_TOOL_SAFETY_BLOCK" || dispatch != "not_observed" || strings.Contains(next, "follow the server") {
		t.Fatalf("client refusal attributed to MCPshka: %s %s %s %q", class, code, dispatch, next)
	}
}
