package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"mcpshka/internal/agents"
	"mcpshka/internal/repopath"
	"mcpshka/internal/roadmaprun"
	"mcpshka/internal/rootbroker"
)

var checkAgentRootAdmission = rootbroker.RunAdminSession

// worktreeRepo maps a task worktree to the repository the owner consented
// to; consent names repositories only (tests swap it).
var worktreeRepo = repopath.Worktree
var newAgentTaskLease = func() (agents.TaskLease, error) {
	auth, err := os.ReadFile("/etc/mcpshka/mcp-auth-header")
	if err != nil {
		return nil, err
	}
	return roadmaprun.HTTPLease{URL: "http://127.0.0.1:8765/mcp", Auth: strings.TrimSpace(string(auth))}, nil
}

// Recheck the original owner grant on quota successors and every roadmap
// attempt; revocation stops new admissions without killing an existing writer.
func checkAgentAttempt(ctx context.Context, agent, path string) error {
	session, owner := os.Getenv("MCPSHKA_AGENT_SESSION_ID"), os.Getenv("MCPSHKA_AGENT_OWNER")
	if session == "" || owner == "" {
		return errors.New("native_only: missing owner-approved runner session")
	}
	if repo := worktreeRepo(path); repo != "" {
		path = repo
	}
	_, err := checkAgentRootAdmission(ctx, []string{"agent-admission", session, owner, path, agent})
	return err
}

func runAuthorizedAgentTask(ctx context.Context, agent, path, prompt, report string) (string, error) {
	if err := checkAgentAttempt(ctx, agent, path); err != nil {
		return "", err
	}
	lease, err := newAgentTaskLease()
	if err != nil {
		return "", err
	}
	return agents.RunWithTaskLease(ctx, agent, path, prompt, report, lease, agents.RunInUnit)
}

func authorizedRoadmapAgent(report string) func(context.Context, string, string, string) (string, error) {
	run := roadmaprun.AgentRunner(report)
	return func(ctx context.Context, agent, path, prompt string) (string, error) {
		if err := checkAgentAttempt(ctx, agent, path); err != nil {
			return "", err
		}
		return run(ctx, agent, path, prompt)
	}
}
