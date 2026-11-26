package rootbroker

import "testing"

func TestAgentConsentPathsCoverSeveralRepositories(t *testing.T) {
	c := &AgentConsent{Path: "/root/repos/mcpshka", Paths: []string{"/root/repos/diag", "/root/repos/lane"}, Agents: []string{"codex"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/root/repos/mcpshka", "/root/repos/diag", "/root/repos/lane"} {
		if !c.Covers(p) {
			t.Fatalf("%s not covered", p)
		}
	}
	if c.Covers("/root/repos/kopilka") || (*AgentConsent)(nil).Covers("/root/repos/diag") {
		t.Fatal("covers too much")
	}
	for _, bad := range []*AgentConsent{
		{Path: "/root/repos/diag", Paths: []string{"/root/repos/diag"}, Agents: []string{"codex"}},
		{Path: "/root/repos/diag", Paths: []string{"/root/repos/../etc"}, Agents: []string{"codex"}},
		{Path: "/root/repos/diag", Paths: []string{"/root/repos/mcpshka-controlled"}, Agents: []string{"codex"}},
		{Path: "/root/repos/a", Paths: []string{"/root/repos/b", "/root/repos/c", "/root/repos/d", "/root/repos/e", "/root/repos/f"}, Agents: []string{"codex"}},
	} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
