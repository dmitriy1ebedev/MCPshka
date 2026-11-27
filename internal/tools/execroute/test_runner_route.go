package execroute

import (
	"path/filepath"

	"mcpshka/internal/failure"
)

// TestRunnerRoute refuses test processes through generic exec_start in favor
// of resource-bounded project_test with scoped package selection.
func TestRunnerRoute(command string, args []string, workdir string) error {
	base := filepath.Base(command)
	isTest := len(args) > 0 && (base == "go" && args[0] == "test" ||
		(base == "npm" || base == "pnpm" || base == "yarn") && args[0] == "test" ||
		base == "python3" && len(args) > 1 && args[0] == "-m" && args[1] == "pytest") ||
		base == "pytest"
	if !isTest {
		return nil
	}
	e := failure.NewHold(failure.PolicyDenied, "TEST_ROUTE_PROJECT_TEST", "mcpserver.exec_start",
		"tests run through project_test (packages and run select a subset; no arguments run the project profile), not exec_start", nil)
	e.NoEffect = true
	e.NextSafeAction = failure.Action{Tool: "project_test", Arguments: map[string]any{"path": workdir}}
	return e
}
