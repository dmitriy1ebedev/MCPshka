package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mcpshka/internal/ownerweb"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/agents"
	"mcpshka/internal/authn"
	"mcpshka/internal/rootbroker"
)

func TestAgentAdmissionRejectsBeforeAnyMutationReceipt(t *testing.T) {
	prior := agentAdmissionBroker
	defer func() { agentAdmissionBroker = prior }()
	allowed, effects := false, 0
	agentAdmissionBroker = func(_ context.Context, session, owner, path string, agents []string) error {
		if session != strings.Repeat("a", 32) || owner != "owner-a" || path != "/root/repos/mcpshka" || len(agents) != 1 || agents[0] != "codex" {
			t.Fatal("unbound admission", session, owner, path, agents)
		}
		if !allowed {
			return errors.New("native_only: owner did not authorize this agent through Face ID")
		}
		return nil
	}
	r := dynamicRegistryFixture(t)
	r.readers["state_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	r.readers["agent_task_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	addTool(r, &mcp.Tool{Name: "agent_task"}, func(context.Context, *mcp.CallToolRequest, AgentTaskInput) (*mcp.CallToolResult, map[string]any, error) {
		effects++
		return nil, map[string]any{"id": "fixture-task"}, nil
	})
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-a", Role: "operator", Scopes: []string{"agent_task"}})
	input, _ := object(AgentTaskInput{SessionID: strings.Repeat("a", 32), Agent: "codex", Path: "/root/repos/mcpshka", Prompt: "fixture only", IdempotencyKey: "admission-retry"})
	_, _, err := r.invokers["agent_task"](ctx, input)
	if err == nil || effects != 0 {
		t.Fatal("native_only admitted an effect", effects, err)
	}
	var classified *classifiedError
	if !errors.As(classifyFailure(err), &classified) || classified.Class != FailPolicyDenied {
		t.Fatal("owner prohibition misclassified", classifyFailure(err))
	}
	sum := sha256.Sum256([]byte("admission-retry"))
	if _, err := r.store.Lookup("contract:" + hex.EncodeToString(sum[:])); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("policy rejection created a held receipt", err)
	}
	allowed = true
	for range 2 {
		if _, _, err := r.invokers["agent_task"](ctx, input); err != nil {
			t.Fatal("same unused key failed after real approval", err)
		}
	}
	if effects != 1 {
		t.Fatal("approved retry repeated an effect", effects)
	}
}

func TestAgentAdmissionMapsEveryDelegatingTool(t *testing.T) {
	prior := agentAdmissionBroker
	defer func() { agentAdmissionBroker = prior }()
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-a", Role: "operator"})
	for _, tc := range []struct {
		name, path string
		agents     []string
		input      map[string]any
	}{
		{"agent_task", "/root/repos/diag", []string{"claude"}, map[string]any{"path": "/root/repos/diag", "agent": "claude"}},
		{"roadmap_run", "/root/repos/diag", []string{"codex", "claude"}, map[string]any{"path": "/root/repos/diag", "allowed_agents": []any{"codex", "claude"}}},
		{"cloud_agent_task", "/root/repos/mcpshka", []string{"codex_cloud"}, map[string]any{"provider": "codex_cloud"}},
		{"issue_dispatch", "/root/repos/mcpshka", []string{"codex"}, map[string]any{"agent": "codex"}},
		{"tool_forge", "/root/repos/mcpshka-forge", []string{"claude"}, map[string]any{"action": "build", "agent": "claude"}},
	} {
		calls := 0
		agentAdmissionBroker = func(_ context.Context, session, owner, path string, agents []string) error {
			calls++
			if path != tc.path || strings.Join(agents, ",") != strings.Join(tc.agents, ",") || owner != "owner-a" || session != "fixture" {
				t.Fatal("wrong consent scope", tc.name, path, agents)
			}
			return errors.New("native_only")
		}
		tc.input["admin_session_id"] = "fixture"
		if err := toolPreflights[tc.name](ctx, tc.input); err == nil || calls != 1 {
			t.Fatal("delegation preflight missing", tc.name, err, calls)
		}
	}
	agentAdmissionBroker = func(context.Context, string, string, string, []string) error {
		t.Fatal("native tool_forge requested an agent")
		return nil
	}
	if err := toolPreflights["tool_forge"](ctx, map[string]any{"action": "build"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConsentBeginArgumentsAndPortal(t *testing.T) {
	old := adminBeginArgs("session", "owner", "purpose")
	if len(old) != 3 {
		t.Fatal("legacy begin changed", old)
	}
	consent := &rootbroker.AgentConsent{Path: "/root/repos/mcpshka", Agents: []string{"codex"}}
	args := adminBeginArgs("session", "owner", "purpose", consent)
	if len(args) != 4 || !strings.Contains(args[3], `"agents":["codex"]`) {
		t.Fatal("scope was not sent to root", args)
	}
	for _, required := range []string{"data.agent_consent.agents.join", "data.agent_consent.path", "Дополнительные ИИ запрещены (native_only)"} {
		if !strings.Contains(ownerweb.PasskeyHTML, required) {
			t.Fatal("owner cannot see delegation scope", required)
		}
	}
}

// 14.11.2026: consent for /root/repos/mcpshka must cover its worktrees — the
// preflight rejected a worktree path before agent_task could map it.
func TestAgentAdmissionWorktreeUsesRepositoryConsent(t *testing.T) {
	wd, _ := os.Getwd()
	wt := strings.TrimSuffix(wd, "/internal/mcpserver")
	repo := ""
	if scope, err := agents.ScopeArgs(wt); err == nil && len(scope) == 2 {
		repo = scope[1]
	}
	if repo == "" {
		t.Skip("tests do not run inside a linked worktree of /root/repos")
	}
	prior := agentAdmissionBroker
	defer func() { agentAdmissionBroker = prior }()
	var got string
	agentAdmissionBroker = func(_ context.Context, _, _, path string, _ []string) error {
		got = path
		return nil
	}
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-a", Role: "operator", Scopes: []string{"agent_task"}})
	err := toolPreflights["agent_task"](ctx, map[string]any{"path": wt, "agent": "codex", "admin_session_id": strings.Repeat("a", 32)})
	if err != nil || got != repo {
		t.Fatal("worktree admission not bound to its repository", got, repo, err)
	}
}
