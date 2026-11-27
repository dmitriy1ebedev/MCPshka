package toolcontract

import "testing"

func TestScriptMutableFollowsTheContract(t *testing.T) {
	if k, err := ScriptMutable("fs_write", map[string]any{"idempotency_key": "k"}); err != nil || k != "k" {
		t.Fatalf("contract tool with key: %q %v", k, err)
	}
	for name, args := range map[string]map[string]any{
		"fs_write":     {},                                  // no key
		"fs_read":      {"idempotency_key": "k"},            // read-only: mcp.call
		"state_backup": {"idempotency_key": "k"},            // approval-only
		"exec_start":   {"idempotency_key": "k"},            // dispatcher
		"code_run":     {"idempotency_key": "k"},
	} {
		if _, err := ScriptMutable(name, args); err == nil {
			t.Fatalf("%s %v must be refused", name, args)
		}
	}
}
