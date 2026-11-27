package projectops

import (
	"testing"

	"mcpshka/internal/projectprofile"
)

func TestMCP216OnlyRegisteredNodeBrowserTestsAutoSeal(t *testing.T) {
	base := projectprofile.Plan{
		Root: "/root/repos/ug2", Source: "registered", Profile: "ug2",
		Workflow: "browser", Command: "node", Args: []string{"tests/browser_smoke.mjs"},
		TimeoutSeconds: 300, OutputLimitBytes: 32768,
	}
	if !sealedBrowserFallback(base, "requires privileged command") {
		t.Fatal("UG2 registered browser profile was not routed through sealed Workbench")
	}
	for _, extra := range []string{"--require=./scripts/install.js", "--eval=process.exit(0)", "&&", "scripts/mutate.js"} {
		modified := base
		modified.Args = []string{"tests/browser_smoke.mjs", extra}
		if sealedBrowserFallback(modified, "requires privileged command") {
			t.Fatalf("unreviewed extra Node.js argv passed implicit admission: %s", extra)
		}
	}
	tests := []struct {
		name, source, workflow, command, firstArg, reason string
	}{
		{"built in", "builtin", "browser", "node", "tests/browser_smoke.mjs", "privileged"},
		{"not browser", "registered", "package", "node", "tests/browser_smoke.mjs", "privileged"},
		{"raw python", "registered", "browser", "python3", "tests/browser_smoke.mjs", "privileged"},
		{"outside tests", "registered", "browser", "node", "scripts/install.mjs", "privileged"},
		{"not a module", "registered", "browser", "node", "tests/launch.sh", "privileged"},
		{"relative escape", "registered", "browser", "node", "tests/../../private/browser.mjs", "privileged"},
		{"explicit deny", "registered", "browser", "node", "tests/browser_smoke.mjs", "explicit denylist"},
	}
	for _, tt := range tests {
		p := base
		p.Source = tt.source
		p.Workflow = tt.workflow
		p.Command = tt.command
		p.Args = []string{tt.firstArg}
		if sealedBrowserFallback(p, tt.reason) {
			t.Fatalf("%s unexpectedly routed through auto sealed executor", tt.name)
		}
	}
}
