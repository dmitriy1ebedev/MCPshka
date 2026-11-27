package agents

import (
	"context"
	"errors"
	"mcpshka/internal/heavyqueue"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCodex puts a codex executable on PATH that prints out and exits code.
func fakeCodex(t *testing.T, out string, code int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '" + out + "'\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunClassifiesModelCapacity(t *testing.T) {
	fakeCodex(t, "ERROR: Selected model is at capacity. Please try a different model.", 1)
	_, err := Run(context.Background(), Codex, t.TempDir(), "review", filepath.Join(t.TempDir(), "r.md"))
	var c *CapacityError
	if !errors.As(err, &c) || !strings.Contains(err.Error(), "transient") {
		t.Fatalf("capacity refusal reported as %v", err)
	}
	if _, ok := IsQuota(err); ok {
		t.Fatal("capacity is not an account usage limit")
	}
}

func TestRunKeepsOtherFailuresAndCleanExits(t *testing.T) {
	fakeCodex(t, "boom", 1)
	if _, err := Run(context.Background(), Codex, t.TempDir(), "x", filepath.Join(t.TempDir(), "r.md")); err == nil || errors.As(err, new(*CapacityError)) {
		t.Fatalf("plain failure misclassified: %v", err)
	}
	// A finished review that merely quotes the message is done, not failed.
	fakeCodex(t, "found: model is at capacity handling is missing", 0)
	if _, err := Run(context.Background(), Codex, t.TempDir(), "x", filepath.Join(t.TempDir(), "r.md")); err != nil {
		t.Fatalf("clean exit misclassified: %v", err)
	}
	// A usage limit still wins over capacity wording.
	fakeCodex(t, "model is at capacity; You have hit your usage limit. Try again at 1:42 AM.", 1)
	if _, err := Run(context.Background(), Codex, t.TempDir(), "x", filepath.Join(t.TempDir(), "r.md")); !errors.As(err, new(*QuotaError)) {
		t.Fatalf("usage limit lost: %v", err)
	}
}

func TestRunTaskRecordsCapacityAsExplainedFailure(t *testing.T) {
	previous := taskHeavyQueue
	t.Cleanup(func() { taskHeavyQueue, taskDir = previous, TaskDir })
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	taskDir = t.TempDir()
	id := "00112233aabbccdd"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("review"), 0o600); err != nil {
		t.Fatal(err)
	}
	busy := func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
		return "ERROR: Selected model is at capacity.", &CapacityError{Agent: agent}
	}
	got, err := RunTask(context.Background(), id, Codex, "/root/repos/diag", busy)
	if err != nil || got.State != "failed" || !strings.Contains(got.Error, "at capacity") || got.Error == "exit status 1" {
		t.Fatal(got, err)
	}
}
