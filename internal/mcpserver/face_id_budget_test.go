package mcpserver

import "testing"

// MCP-042: the owner's everyday chain (read config and logs, edit an
// allowlisted file, check config, restart an allowed service or container,
// update an HA entity) costs at most ONE Face ID — the work session — while
// plan-bound tools keep their own per-plan confirmation.
func TestEverydayChainNeedsAtMostOneFaceID(t *testing.T) {
	c := catalogClient(t, nil)
	chain := []string{"fs_read", "journal_read", "fs_patch", "ha_check_config", "service_action", "docker_action", "ha_entity_update"}
	prompts := 0
	for _, tool := range chain {
		r := catalogCall(t, c, "admin_coverage", map[string]any{"tool": tool})
		switch r["needs"] {
		case "none":
		case "face_id_session":
			prompts = 1 // one session covers every later call of the chain
		default:
			t.Errorf("%s needs %v (%v): a second Face ID inside one work session", tool, r["needs"], r["action"])
		}
	}
	if prompts > 1 {
		t.Fatalf("chain needs %d Face ID prompts", prompts)
	}
	for tool := range ownFaceIDGrant {
		if r := catalogCall(t, c, "admin_coverage", map[string]any{"tool": tool}); r["needs"] != "plan_face_id" {
			t.Fatalf("%s lost its plan-bound confirmation: %v", tool, r["needs"])
		}
		break
	}
}
