package mcpserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStableGatewaySixOperations(t *testing.T) {
	want := []string{"capabilities_bootstrap", "tool_resolve", "tool_invoke", "gateway_status", "gateway_wait", "gateway_reconcile"}
	if !reflect.DeepEqual(coreTools, want) {
		t.Fatalf("stable gateway surface = %v, want %v", coreTools, want)
	}
}

func TestGatewayStatusUsesReadOnlyBoundary(t *testing.T) {
	s := toolInvokeSession(t)
	text, bad := callText(t, s, "gateway_status", map[string]any{"name": "mcpshka_guide", "arguments": map[string]any{"topic": "rules"}})
	if bad || !strings.Contains(text, "rules") {
		t.Fatalf("read probe: %s", text)
	}
	text, bad = callText(t, s, "gateway_status", map[string]any{"name": "fs_write", "arguments": map[string]any{}})
	if !bad || !strings.Contains(text, "read-only") {
		t.Fatalf("mutation through status: %s", text)
	}
}

func TestGatewayWaitPreservesUnknown(t *testing.T) {
	s := toolInvokeSession(t)
	text, bad := callText(t, s, "gateway_wait", map[string]any{"name": "mcpshka_guide", "arguments": map[string]any{"topic": "rules"}, "timeout_seconds": 1})
	if bad {
		t.Fatal(text)
	}
	var out struct{ State string }
	if json.Unmarshal([]byte(text), &out) != nil || out.State != "UNKNOWN" {
		t.Fatalf("nonterminal evidence invented completion: %s", text)
	}
}
