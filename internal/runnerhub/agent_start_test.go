package runnerhub

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRunnerAutostartShellSyntax(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/bin/sh", "-n", "setup.sh").CombinedOutput(); err != nil {
		t.Fatalf("setup syntax: %s %v", out, err)
	}
	script := "../../.devcontainer/start-runner.sh"
	if _, err := os.Stat(script); os.IsNotExist(err) {
		t.Log("no .devcontainer in this snapshot: autostart syntax not checked")
		return
	}
	if out, err := exec.CommandContext(ctx, "/bin/sh", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("autostart syntax: %s %v", out, err)
	}
}
