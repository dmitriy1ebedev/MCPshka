package runnerhub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAgentDuplicateStartExitsBeforeNetwork(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	conf := filepath.Join(t.TempDir(), "runner.json")
	lock, err := os.OpenFile(conf+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, python, "-B", "agent.py", "--url", "https://fixture.invalid", "--conf", conf).CombinedOutput(); err != nil {
		t.Fatalf("duplicate started registration: %s %v", out, err)
	}
	// The sealed module snapshot carries no dot directories (0.18.0 release:
	// this check failed there while the agent part passed); the repository
	// checkout still checks the autostart script.
	script := "../../.devcontainer/start-runner.sh"
	if _, err := os.Stat(script); os.IsNotExist(err) {
		t.Log("no .devcontainer in this snapshot: autostart syntax not checked")
		return
	}
	if out, err := exec.CommandContext(ctx, "/bin/sh", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("autostart syntax: %s %v", out, err)
	}
}
