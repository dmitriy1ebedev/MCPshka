package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mcpshka/internal/agents"
)

func TestAgentAttemptRechecksOriginalOwnerAndRevocation(t *testing.T) {
	prior, priorLease := checkAgentRootAdmission, newAgentTaskLease
	defer func() { checkAgentRootAdmission, newAgentTaskLease = prior, priorLease }()
	newAgentTaskLease = func() (agents.TaskLease, error) {
		t.Fatal("denied runner read lease credentials or acquired authority")
		return nil, nil
	}
	t.Setenv("MCPSHKA_AGENT_SESSION_ID", "original-session")
	t.Setenv("MCPSHKA_AGENT_OWNER", "owner-a")
	calls := 0
	denied := errors.New("native_only: revoked")
	checkAgentRootAdmission = func(_ context.Context, args []string) ([]byte, error) {
		calls++
		if strings.Join(args, "|") != "agent-admission|original-session|owner-a|/root/repos/mcpshka|codex" {
			t.Fatal("runner expanded permission", args)
		}
		if calls > 1 {
			return nil, denied
		}
		return []byte(`{"state":"authorized"}`), nil
	}
	if err := checkAgentAttempt(context.Background(), "codex", "/root/repos/mcpshka"); err != nil {
		t.Fatal(err)
	}
	// No actual agent is started: both public runner callbacks stop at root denial.
	if _, err := runAuthorizedAgentTask(context.Background(), "codex", "/root/repos/mcpshka", "fixture", "unused"); !errors.Is(err, denied) {
		t.Fatal("quota successor ignored revocation", err)
	}
	if _, err := authorizedRoadmapAgent("unused")(context.Background(), "codex", "/root/repos/mcpshka", "fixture"); !errors.Is(err, denied) {
		t.Fatal("roadmap attempt ignored revocation", err)
	}
	t.Setenv("MCPSHKA_AGENT_SESSION_ID", "")
	if err := checkAgentAttempt(context.Background(), "codex", "/root/repos/mcpshka"); err == nil || calls != 3 {
		t.Fatal("unbound CLI runner reached root", err, calls)
	}
}

// Each attempt of a task in its own worktree re-checks the owner's consent
// for the repository, not the worktree path consent can never name.
func TestAgentAttemptChecksWorktreeRepository(t *testing.T) {
	prior, priorRepo := checkAgentRootAdmission, worktreeRepo
	defer func() { checkAgentRootAdmission, worktreeRepo = prior, priorRepo }()
	t.Setenv("MCPSHKA_AGENT_SESSION_ID", "s")
	t.Setenv("MCPSHKA_AGENT_OWNER", "o")
	worktreeRepo = func(p string) string {
		if p == "/root/repos/wt/mcpshka-agent-1" {
			return "/root/repos/mcpshka"
		}
		return ""
	}
	var got string
	checkAgentRootAdmission = func(_ context.Context, args []string) ([]byte, error) {
		got = args[3]
		return []byte(`{}`), nil
	}
	if err := checkAgentAttempt(context.Background(), "codex", "/root/repos/wt/mcpshka-agent-1"); err != nil || got != "/root/repos/mcpshka" {
		t.Fatal("worktree consent path", got, err)
	}
	if err := checkAgentAttempt(context.Background(), "codex", "/root/repos/other"); err != nil || got != "/root/repos/other" {
		t.Fatal("repository path changed", got, err)
	}
}
