package projectlease

import (
	"os"
	"strings"
	"testing"
)

// A controller running inside the repository it guards is itself a live holder,
// so a stale lease on that repository could never be proven abandoned (MCP-144).
func TestControllerUnitDoesNotRunInsideProtectedRepository(t *testing.T) {
	raw, err := os.ReadFile("../../packaging/systemd/mcpshka.service")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\nWorkingDirectory=/root/repos\n") {
		t.Fatal("mcpshka.service must use WorkingDirectory=/root/repos, outside any project")
	}
}
