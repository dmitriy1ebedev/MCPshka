package mcpserver

import (
	"errors"
	"strings"
	"testing"
)

// MCP-043: every POLICY_DENIED names its risk and a concrete permitted route.
func TestPolicyDenialCarriesRiskAndAllowedRoute(t *testing.T) {
	cases := map[string]string{
		"write /etc/fstab denied: path outside allowlist": "fs_patch",
		"restart of protected unit zigbee2mqtt denied":    "service_action",
		"policy denied: nft ruleset change":               "network_stage_apply",
		"policy: arbitrary command as root shell":         "approved_task_prepare",
		"token lacks scope mcpshka:admin":                 "admin_session_prepare",
		"denied by policy":                                "tool_resolve",
	}
	for msg, want := range cases {
		got := classifyFailure(errors.New(msg)).Error()
		if !strings.Contains(got, "[POLICY_DENIED]") || !strings.Contains(got, "risk: ") || !strings.Contains(got, want) {
			t.Errorf("%q -> %s (want route %s)", msg, got, want)
		}
	}
	// Other classes keep their own envelope.
	if got := classifyFailure(errors.New("request timed out")).Error(); strings.Contains(got, "allowed_route") {
		t.Fatalf("non-denial got denial route: %s", got)
	}
}
