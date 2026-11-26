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

func TestResetParsingAndCommands(t *testing.T) {
	now := time.Date(2026, 10, 21, 20, 46, 0, 0, time.Local)
	r := parseReset("ERROR: You’ve hit your usage limit. ... try again at 10:17 PM.", now)
	if r.Hour() != 22 || r.Minute() != 17 || r.Day() != 21 {
		t.Fatal("codex reset", r)
	}
	r = parseReset("Claude AI usage limit reached. Your limit resets 3am", now)
	if r.Hour() != 3 || r.Day() != 22 {
		t.Fatal("claude reset", r)
	}
	r = parseReset("You've hit your session limit · resets 1:50am (Europe/Moscow)", now)
	if r.Hour() != 1 || r.Minute() != 50 || r.Day() != 22 {
		t.Fatal("claude session reset", r)
	}
	if !quotaText.MatchString("You've hit your session limit") {
		t.Fatal("Claude session limit not recognised as quota")
	}
	if !parseReset("nothing", now).IsZero() {
		t.Fatal("phantom reset")
	}
	codex, _ := Command(context.Background(), Codex, "/root/repos/diag", "p", "/tmp/r")
	if !strings.Contains(strings.Join(codex.Args, " "), "exec -s danger-full-access") {
		t.Fatal(codex.Args)
	}
	claude, _ := Command(context.Background(), Claude, "/root/repos/diag", "p", "")
	args := strings.Join(claude.Args, " ")
	if claude.Dir != "/root/repos/diag" || !strings.Contains(args, "--permission-mode auto") || strings.Contains(args, "dangerously") {
		t.Fatal("claude must run in auto mode, never bypass", args)
	}
	if _, err := Command(context.Background(), "gpt", "/", "p", ""); err == nil {
		t.Fatal("unknown agent accepted")
	}
	q := &QuotaError{Agent: Codex, Reset: r}
	if got, ok := IsQuota(error(q)); !ok || got.Agent != Codex {
		t.Fatal("quota error not recognised")
	}
}

func TestMCP226ExpiredOwnerGrantKeepsPartialEffectsUnknown(t *testing.T) {
	for _, line := range []string{
		"MCPshka: AUTH_REQUIRED", "admin session expired",
		"admin_session_id expired", "Face ID session expired",
	} {
		if !expiredOwnerSessionFinal(line) {
			t.Fatalf("missed grant expiry: %q", line)
		}
	}
	for _, line := range []string{
		"token expired", "connection timed out", "usage limit reached",
		"README mentions admin session expiration", "auth_required in an example",
	} {
		if expiredOwnerSessionFinal(line) {
			t.Fatalf("ordinary output confused with owner grant expiry: %q", line)
		}
	}
	oldQueue, oldDir := taskHeavyQueue, taskDir
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	taskDir = t.TempDir()
	t.Cleanup(func() { taskHeavyQueue, taskDir = oldQueue, oldDir })
	id := "1234567890abcdef"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("fixture task"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := func(context.Context, string, string, string, string) (string, error) {
		return "partial edits may exist", errors.Join(errors.New("authorization expired"), errAttemptUnknown)
	}
	got, err := RunTask(context.Background(), id, Claude, "/root/repos/diag", fake)
	if err != nil || got.State != "unknown" || !strings.Contains(got.NextAction, "reconcile") || !strings.Contains(got.Tail, "partial edits") {
		t.Fatalf("expired approval silently retried or marked failed: %+v %v", got, err)
	}
	persisted, err := ReadTask(id)
	if err != nil || persisted.State != "unknown" {
		t.Fatalf("unknown outcome not durable: %+v %v", persisted, err)
	}
}

func TestRunTaskRecordsOutcome(t *testing.T) {
	previous := taskHeavyQueue
	defer func() { taskHeavyQueue = previous }()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	taskDir = t.TempDir()
	defer func() { taskDir = TaskDir }()
	id := "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("сделай X"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
		if agent != Claude || prompt != "сделай X" || dir != "/root/repos/diag" {
			t.Fatal("task not delegated as asked", agent, prompt, dir)
		}
		return "готово", nil
	}
	got, err := RunTask(context.Background(), id, Claude, "/root/repos/diag", run)
	if err != nil || got.State != "done" || got.Tail != "готово" {
		t.Fatal(got, err)
	}
	if read, err := ReadTask(id); err != nil || read.State != "done" {
		t.Fatal("task record not persisted", read, err)
	}
	quota := func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
		return "", &QuotaError{Agent: agent, Reset: time.Now().Add(time.Hour)}
	}
	got, _ = RunTask(context.Background(), id, Codex, "/root/repos/diag", quota)
	if got.State != "quota" || got.ResetAt.IsZero() {
		t.Fatal("quota not recorded", got)
	}
	if _, err := ReadTask("../x"); err == nil {
		t.Fatal("path id accepted")
	}
}
