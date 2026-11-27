package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAbortPreparationPreservesDirtyWorktree(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "proj")
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "base"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	oldRoot := WorktreeRoot
	WorktreeRoot = filepath.Join(dir, "wt")
	taskDir := filepath.Join(dir, "tasks")
	defer func() { WorktreeRoot = oldRoot }()
	if err := os.MkdirAll(taskDir, 0700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef"
	prompt := filepath.Join(taskDir, id+".prompt")
	if err := os.WriteFile(prompt, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := OwnWorktree(context.Background(), repo, id)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(path, "keep")
	if err := os.WriteFile(artifact, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := abortPreparation(context.Background(), repo, id, true, taskDir); err == nil {
		t.Fatal("dirty tree removed")
	}
	if raw, err := os.ReadFile(artifact); err != nil || string(raw) != "preserve" {
		t.Fatal(err)
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	if err := abortPreparation(context.Background(), repo, id, true, taskDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("worktree retained", err)
	}
	if _, err := os.Stat(prompt); !os.IsNotExist(err) {
		t.Fatal("prompt retained", err)
	}
}
