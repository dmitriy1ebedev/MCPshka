package mcpserver

import (
	"context"
	"errors"
	"strings"

	agentscope "mcpshka/internal/agents"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/toolforge"
)

type agentAdmissionCall func(context.Context, string, string, string, []string) error

var agentAdmissionBroker agentAdmissionCall = func(ctx context.Context, session, owner, path string, agents []string) error {
	out, err := runAdminSession[struct {
		State string `json:"state"`
	}](ctx, "agent-admission", session, owner, path, strings.Join(agents, ","))
	if err != nil {
		return err
	}
	if out.State != "authorized" {
		return errors.New("native_only: agent admission was not confirmed by root")
	}
	return nil
}

func init() {
	for _, name := range []string{"agent_task", "roadmap_run", "cloud_agent_task", "issue_dispatch", "tool_forge"} {
		name := name
		toolPreflights[name] = func(ctx context.Context, input map[string]any) error {
			if name == "tool_forge" && (input["action"] != "build" || input["agent"] == "" || input["agent"] == nil) {
				return nil
			}
			owner, err := adminOwner(ctx)
			if err != nil {
				return err
			}
			path, _ := input["path"].(string)
			session, _ := input["admin_session_id"].(string)
			agent, _ := input["agent"].(string)
			agents := []string{agent}
			if name == "issue_dispatch" && path == "" {
				path = "/root/repos/mcpshka"
			}
			if name == "tool_forge" {
				path = toolforge.ForgeWorktree
			}
			if name == "cloud_agent_task" {
				path = "/root/repos/mcpshka"
				agent, _ = input["provider"].(string)
				agents = []string{agent}
			}
			if name == "agent_task" {
				// Consent for a repository covers its worktrees (14.11: a
				// worktree path failed here before the tool could map it).
				scope, err := agentscope.ScopeArgs(path)
				if err != nil {
					return err
				}
				path = scope[len(scope)-1]
			}
			if name == "roadmap_run" {
				agents = nil
				switch names := input["allowed_agents"].(type) {
				case []string:
					agents = append(agents, names...)
				case []any:
					for _, v := range names {
						name, ok := v.(string)
						if !ok {
							return errors.New("explicit allowed_agents required")
						}
						agents = append(agents, name)
					}
				}
			}
			if err := (&rootbroker.AgentConsent{Path: path, Agents: agents}).Validate(); err != nil {
				return err
			}
			return agentAdmissionBroker(ctx, session, owner, path, agents)
		}
	}
}
