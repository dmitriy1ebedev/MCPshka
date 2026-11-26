package agentlogin

import "testing"

func TestParseDeviceLogin(t *testing.T) {
	out := "Welcome to Codex\n\x1b[1mFollow these steps\x1b[0m\n1. Open this link in your browser\n   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\n2. Enter this one-time code \x1b[90m(expires in 15 minutes)\x1b[0m\n   \x1b[94mX27C-NNCH8\x1b[0m\n"
	link, code := parseDeviceLogin(out)
	if link != "https://auth.openai.com/codex/device" || code != "X27C-NNCH8" {
		t.Fatalf("got %q %q", link, code)
	}
	if l, c := parseDeviceLogin("see https://evil.example/x code ABCD-12345"); l != "" || c != "ABCD-12345" {
		t.Fatalf("foreign link must be ignored: %q %q", l, c)
	}
}

func TestAgentLoginRejectsUnknownActions(t *testing.T) {
	for _, a := range [][]string{nil, {"codex-logout"}, {"codex-begin", "x"}} {
		if _, err := Run(t.Context(), a); err == nil {
			t.Fatalf("%v accepted", a)
		}
	}
}

func TestCodexLoginStartsThroughEnv(t *testing.T) {
	a := codexLoginArgv()
	if a[0] != "/usr/bin/env" || a[1] != "HOME=/root" || a[3] != codexBinary || a[len(a)-1] != "--device-auth" {
		t.Fatalf("argv %v", a)
	}
}
