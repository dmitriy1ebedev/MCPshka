package rootbroker

import (
	"mcpshka/internal/executor"
	"mcpshka/internal/heavyqueue"
	"strings"
	"testing"
)

func TestHeavyWorkersShareSchedulingClass(t *testing.T) {
	id := strings.Repeat("a", 32)
	units := map[string][]string{
		"executor":     executorUnitArgs(id, id),
		"sealed-test":  projectTestUnitArgs(id, id),
		"module-build": moduleTestUnitArgs(id, id),
		"integration":  integrationUnitArgs(executor.IntegrationLaunch{}),
		"agent-runner": agentTaskServiceArgs([]string{"agent-task", id, "owner", "0123456789abcdef", "codex", "/root/repos/example"}),
		"console":      codexSessionServiceArgs(id),
		"roadmap":      roadmapRunServiceArgs([]string{"roadmap-run", id, "owner", "/root/repos/example", "1", "1"}, id),
	}
	for _, action := range []string{"integration-test-prepare", "release-build-prepare", "integration-ci-prepare", "integration-gitverse-prepare"} {
		_, args, err := integrationAdminServiceArgs([]string{action, id, "owner:test", "/root/repos/mcpshka", strings.Repeat("a", 40)})
		if err != nil {
			t.Fatal(err)
		}
		units[action] = args
	}
	for name, args := range units {
		for _, property := range []string{"Slice=" + heavyqueue.HeavySlice, "CPUWeight=20", "IOWeight=10", "Nice=10", "IOSchedulingClass=idle"} {
			n := 0
			for _, a := range args {
				if a == "--property="+property {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s: %s count=%d in %q", name, property, n, args)
			}
		}
	}
}
