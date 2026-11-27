package rootbroker

import (
	"strings"
	"testing"
)

func TestRecoveryWorkerAcceptsProjectScopes(t *testing.T) {
	for _, path := range []string{"/root/repos/diag", "/root/repos/registered-project", "/root/repos/mcpshka-lease-recovery"} {
		for _, action := range []string{"preview-legacy-lease", "status-legacy-lease"} {
			args := leaseWorkerArgs(action)
			args[3] = path
			_, command, err := legacyLeaseServiceArgs(args)
			if err != nil {
				t.Fatalf("%s %s: %v", action, path, err)
			}
			if !strings.Contains(strings.Join(command, "\n"), "BindReadOnlyPaths=/root/repos") {
				t.Fatal("project evidence unavailable")
			}
		}
	}
}
