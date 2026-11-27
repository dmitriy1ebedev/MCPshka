package systemdsvc

import "testing"

func TestOwnTransientUnitsAreReadable(t *testing.T) {
	for _, u := range []string{"mcpshka-executor-4f706d16de28c2ebe3f3eb44e0dc5713.service", "mcpshka-integration-admin-1ba6c11f3217d035ddd45aef8804ab3e.service", "mcpshka-release-apply-050.service", "mcpshka-recipe-prepare-abc.service"} {
		if !OwnTransientUnit(u) {
			t.Fatal("own unit not readable:", u)
		}
		if err := (&Service{statusAllowed: map[string]struct{}{}}).requireStatusAllowed(u); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []string{"mcpshka-codex-login.service", "mcpshka.service", "ssh.service", "mcpshka-executor-.service", "x-mcpshka-executor-1.service"} {
		if OwnTransientUnit(u) {
			t.Fatal("foreign or sensitive unit treated as own:", u)
		}
	}
}
