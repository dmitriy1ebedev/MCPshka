package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/distributedci"
)

func TestModuleGateStopsAfterArchlintFailure(t *testing.T) {
	dir := t.TempDir()
	tool := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(tool), 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir)
	t.Setenv("CALLS", calls)
	out, err := exec.Command(moduleTestCommand[0], moduleTestCommand[1:]...).CombinedOutput()
	raw, readErr := os.ReadFile(calls)
	if err == nil || readErr != nil || string(raw) != "test -count=1 ./internal/archlint\n" || !strings.Contains(string(out), distributedci.ArchlintFailed) {
		t.Fatalf("calls=%q output=%q run=%v read=%v", raw, out, err, readErr)
	}
}
