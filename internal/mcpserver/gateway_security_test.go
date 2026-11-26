package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"mcpshka/internal/authn"
)

func TestGatewayReadKeepsTargetAuthorization(t *testing.T) {
	s := catalogClient(t, &authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"gateway_status", "gateway_wait", "mcpshka_guide"}})
	text, bad := callText(t, s, "gateway_status", map[string]any{"name": "mcpshka_guide", "arguments": map[string]any{"topic": "rules"}})
	if bad || !strings.Contains(text, "rules") {
		t.Fatal(text)
	}
	text, bad = callText(t, s, "gateway_status", map[string]any{"name": "fs_read", "arguments": map[string]any{"path": "/not-permitted"}})
	if !bad || !strings.Contains(text, "scope") {
		t.Fatalf("target authorization lost: %s", text)
	}
}
func TestGatewayDeniedInventoryIsBoundedAndSafe(t *testing.T) {
	s := catalogClient(t, &authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"capabilities_bootstrap", "tool_resolve", "capability_inventory", "fs_read"}})
	boot := catalogCall(t, s, "capabilities_bootstrap", map[string]any{})
	if boot["denied_count"] == nil {
		t.Fatal("discover has no denied inventory", boot)
	}
	seen := map[string]bool{}
	cursor := ""
	for n := 0; n < 100; n++ {
		page := catalogCall(t, s, "capability_inventory", map[string]any{"cursor": cursor, "limit": 16})
		rows, ok := page["denied"].([]any)
		if !ok || len(rows) > 16 {
			t.Fatal(page)
		}
		for _, row := range rows {
			r := row.(map[string]any)
			name := r["name"].(string)
			if seen[name] || name == "fs_read" {
				t.Fatalf("bad inventory row: %v", r)
			}
			seen[name] = true
			for _, k := range []string{"reason_code", "required_permission", "next_safe_action"} {
				if r[k] == nil || r[k] == "" {
					t.Fatal(r)
				}
			}
			if r["input_schema"] != nil || r["output_schema"] != nil {
				t.Fatal("denied schema disclosed", r)
			}
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		if next <= cursor {
			t.Fatal("cursor did not advance", page)
		}
		cursor = next
		if n == 99 {
			t.Fatal("inventory never ended")
		}
	}
	if !seen["exec_start"] {
		t.Fatal("denied mutation absent")
	}
	exact := catalogCall(t, s, "tool_resolve", map[string]any{"goal": "exec_start"})
	if exact["code"] != "CAPABILITY_DENIED" || len(exact["candidates"].([]any)) != 0 {
		t.Fatal("denied tool replaced by unrelated suggestion", exact)
	}
}
func TestGatewayReconcileDoesNotReplay(t *testing.T) {
	s := toolInvokeSession(t)
	text, bad := callText(t, s, "gateway_reconcile", map[string]any{"tool": "fs_write", "key": "original-no-receipt"})
	if bad {
		t.Fatal(text)
	}
	var out struct{ Output map[string]any }
	if json.Unmarshal([]byte(text), &out) != nil || out.Output["retry_allowed"] != false {
		t.Fatal(text)
	}
}
func TestGatewayWaitRejectsInvalidBudget(t *testing.T) {
	s := toolInvokeSession(t)
	for _, n := range []int{-1, 46} {
		text, bad := callText(t, s, "gateway_wait", map[string]any{"name": "mcpshka_guide", "timeout_seconds": n})
		if !bad || !strings.Contains(text, "0..45") {
			t.Fatal(text)
		}
	}
}
