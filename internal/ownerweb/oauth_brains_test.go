package ownerweb

import (
	"testing"
	"time"
)

func isolateConnState(t *testing.T) {
	t.Helper()
	old := connStateDir
	connStateDir = t.TempDir()
	t.Cleanup(func() { connStateDir = old })
}

func TestBrainSwitchesGateCallbacks(t *testing.T) {
	isolateConnState(t)
	claude, chatgpt := "https://claude.ai/api/mcp/auth_callback", "https://chatgpt.com/connector_platform_oauth_redirect"
	if !redirectAllowed(claude) || !redirectAllowed(chatgpt) {
		t.Fatal("Claude and ChatGPT must be on by default")
	}
	if err := setBrain("brain-claude", false); err != nil || redirectAllowed(claude) || !redirectAllowed(chatgpt) {
		t.Fatal("switching Claude off", err)
	}
	if clientAllowed(oauthClient{RedirectURIs: []string{claude}}) {
		t.Fatal("a switched-off brain's client still passes")
	}
	kimi := "https://kimi.example/mcp/callback"
	if redirectAllowed(kimi) {
		t.Fatal("unknown callback allowed")
	}
	noteRejected(kimi, "Kimi", time.Now())
	noteRejected("http://insecure.example/cb", "x", time.Now())
	noteRejected("javascript:alert(1)", "x", time.Now())
	var row *ConnStatus
	for _, s := range brainStatuses() {
		if s.Redirect == kimi {
			row = &s
		}
		if s.Redirect == "http://insecure.example/cb" || s.Redirect == "javascript:alert(1)" {
			t.Fatal("non-https callback offered", s.Redirect)
		}
	}
	if row == nil || row.State != "ask" || row.Title != "Kimi" {
		t.Fatal("rejected Kimi callback not offered", row)
	}
	if err := setBrain(row.ID, true); err != nil || !redirectAllowed(kimi) {
		t.Fatal("allowing Kimi", err)
	}
	for i := 0; i < 2*maxBrainExtra; i++ {
		noteRejected("https://spam.example/"+string(rune('a'+i)), "spam", time.Now().Add(time.Duration(i)*time.Second))
	}
	if !redirectAllowed(kimi) || len(loadBrains().Extra) > maxBrainExtra {
		t.Fatal("spam pushed out the allowed brain or overflowed", len(loadBrains().Extra))
	}
	if setBrain("brain-unknown", true) == nil {
		t.Fatal("unknown brain id accepted")
	}
}
