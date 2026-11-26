package projectops

import (
	"path/filepath"
	"strings"

	"mcpshka/internal/projectprofile"
)

// MCP-216: "project_run browser" is the only implicit managed route.
// The profile must be registered or committed with the repository, its
// command must be Node, and the path must be a test script. The Workbench
// performs independent profile-digest/HEAD/argv/isolated-runtime checks.
// Never turn a policy explicit deny into an automatically approved workflow.
func sealedBrowserFallback(plan projectprofile.Plan, policyReason string) bool {
	if plan.Source != "registered" && plan.Source != "repository" {
		return false
	}
	if plan.Workflow != "browser" || policyReason == "explicit denylist" {
		return false
	}
	if filepath.Base(plan.Command) != "node" && filepath.Base(plan.Command) != "nodejs" {
		return false
	}
	// An additional argv entry could turn a reviewed test into an arbitrary
	// Node.js command or change how the test runner executes.
	if len(plan.Args) != 1 || !filepath.IsLocal(plan.Args[0]) ||
		filepath.Clean(plan.Args[0]) != plan.Args[0] ||
		!strings.HasPrefix(plan.Args[0], "tests/") ||
		!strings.HasSuffix(plan.Args[0], ".mjs") {
		return false
	}
	return true
}
