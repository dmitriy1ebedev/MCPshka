package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/adminops"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/agents"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
)

type AgentTaskInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	Agent          string `json:"agent" jsonschema:"codex or claude — the agent that should do the work."`
	Path           string `json:"path" jsonschema:"Repository the agent works in: /root/repos/<name>, or its worktree /root/repos/wt/<name> (the task leases and writes only that worktree; consent for the repository covers it)."`
	Prompt         string `json:"prompt,omitempty" jsonschema:"Self-contained task (up to 64 KiB): goal, files, acceptance; the agent starts with no prior context. With resume_of: optional addition."`
	Worktree       bool   `json:"worktree,omitempty" jsonschema:"true: the task gets its own worktree /root/repos/wt/<repo>-agent-<id> on branch agent/<id> from HEAD and writes only there; the main checkout stays free. Path must be the repository."`
	ResumeOf       string `json:"resume_of,omitempty" jsonschema:"Id of an interrupted, failed, unknown (own unit is probed first) or unscheduled quota task (agent_task_status) to continue: its prompt is reused with an instruction to finish the uncommitted work."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type AgentTaskCancelInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	ID             string `json:"id" jsonschema:"Task id from agent_task."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type AgentTaskListInput struct {
	Path   string `json:"path,omitempty" jsonschema:"Only tasks of this repository."`
	Agent  string `json:"agent,omitempty" jsonschema:"codex or claude."`
	State  string `json:"state,omitempty" jsonschema:"QUEUED, running, done, failed, quota, unknown, cancelled or interrupted."`
	Since  string `json:"since,omitempty" jsonschema:"RFC3339: tasks started at or after."`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page."`
	Limit  int    `json:"limit,omitempty" jsonschema:"1..50, default 20."`
}

type AgentTaskStatusInput struct {
	ID string `json:"id" jsonschema:"Task id from agent_task."`
}

func RegisterAgents(add func(tooldef.Def), d AgentDeps, managers ...*jobs.Manager) {
	closed, nonDestructive := false, false
	add(tooldef.New(&mcp.Tool{
		Name:        "agent_task",
		Description: "Delegate one self-contained task to another coding agent on the host — Codex CLI or Claude Code — under the active Face ID window. Both reach the same MCPshka tools, so Codex can hand work to Claude Code and vice versa (for example when one hits its usage limit). Poll agent_task_status; state quota carries reset_at and resume_at: the runner resumes automatically after a known reset within its budget, with resumed_by linking the successor. Unknown resets require provider evidence. resume_of continues an interrupted or unscheduled quota task.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AgentTaskInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, err := adminops.Owner(ctx)
		if err != nil {
			return nil, nil, err
		}
		if in.Agent != agents.Codex && in.Agent != agents.Claude {
			return nil, nil, errors.New("agent must be codex or claude")
		}
		prompt := in.Prompt
		if in.ResumeOf != "" {
			prev, err := agents.ReadTask(in.ResumeOf)
			if err != nil {
				return nil, nil, err
			}
			prev = agents.MarkInterrupted(prev, d.BootTime())
			if prev.Path != filepath.Clean(in.Path) {
				return nil, nil, errors.New("resume_of belongs to " + prev.Path)
			}
			if err := recoverResumeLease(ctx, prev, in.SessionID, owner, d); err != nil {
				return nil, nil, err
			}
			original, err := agents.ReadPrompt(in.ResumeOf)
			if err != nil {
				return nil, nil, err
			}
			if prompt, err = agents.ResumePrompt(prev, original, in.Prompt); err != nil {
				return nil, nil, err
			}
		}
		if len(prompt) == 0 || len(prompt) > 64<<10 {
			return nil, nil, errors.New("prompt must be 1..65536 bytes")
		}
		out, err := idempotency.Run(d.Store, in.IdempotencyKey, "agent_task", in, func() (map[string]any, error) {
			raw := make([]byte, 8)
			if _, err := rand.Read(raw); err != nil {
				return nil, err
			}
			id := hex.EncodeToString(raw)
			if err := os.MkdirAll(agents.TaskDir, 0750); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(agents.TaskDir, id+".prompt"), []byte(prompt), 0640); err != nil {
				return nil, err
			}
			path := in.Path
			if in.Worktree {
				wt, err := agents.OwnWorktree(ctx, path, id)
				if err != nil {
					return nil, err
				}
				path = wt
			}
			scope, err := agents.ScopeArgs(path)
			if err != nil {
				return nil, err
			}
			return dispatchPrepared(func() (map[string]any, error) {
				return adminops.Run[map[string]any](ctx, append([]string{"agent-task", in.SessionID, owner, id, in.Agent}, scope...)...)
			}, func() error { return agents.AbortPreparation(ctx, in.Path, id, in.Worktree) })
		})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "agent_task_status",
		Description: "State of a delegated agent task: QUEUED (reason and position in the host heavy-task queue), running, done, failed, quota (with reset time) unknown (reconcile its unit before any resume) or interrupted (killed by a host reboot; next_action says how to resume), plus the output tail.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AgentTaskStatusInput) (*mcp.CallToolResult, agents.Task, error) {
		t, err := d.TaskRead(in.ID)
		if t.State == "done" {
			var git *gitops.Service
			var proof func(string, string) (bool, string)
			if len(managers) > 0 && managers[0] != nil {
				m := managers[0]
				git = gitops.New(m.Policy())
				proof = func(root, head string) (bool, string) {
					j, ok := m.SealedTestEvidence(root, head)
					return ok && j.State == jobs.Done && j.ExitCode != nil && *j.ExitCode == 0, j.ID
				}
			}
			t = agents.VerifyCompletion(t, git, proof)
		}
		return nil, agents.WithProvider(agents.MarkInterrupted(t, d.BootTime()), time.Now().UTC()), err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "agent_task_list",
		Description: "Page the durable delegated-agent task records newest first, filtered by path/agent/state/since (server-side; cursor=next_cursor). complete=false means some records were unreadable. Each task carries next_action; open one with agent_task_status or continue it with agent_task resume_of.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AgentTaskListInput) (*mcp.CallToolResult, agents.TaskPage, error) {
		f := agents.ListFilter{Path: in.Path, Agent: in.Agent, State: in.State}
		if in.Since != "" {
			since, err := time.Parse(time.RFC3339, in.Since)
			if err != nil {
				return nil, agents.TaskPage{}, errors.New("since must be RFC3339")
			}
			f.Since = since
		}
		page, err := agents.ListTasks(f, in.Cursor, in.Limit, d.BootTime())
		return nil, page, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "agent_task_cancel",
		Description: "Stop one delegated agent task (running, queued, waiting for quota resume or UNKNOWN) under the active Face ID window. Root stops its runner and attempt units and records state cancelled only after both are observed ended; otherwise nothing changes and the error says UNKNOWN/HOLD. Committed or uncommitted work stays in the repository; inspect git before agent_task resume_of. A finished task is returned unchanged.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AgentTaskCancelInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, err := adminops.Owner(ctx)
		if err != nil {
			return nil, nil, err
		}
		if !agents.ValidTaskID(in.ID) {
			return nil, nil, errors.New("invalid task id")
		}
		out, err := idempotency.Run(d.Store, in.IdempotencyKey, "agent_task_cancel", in, func() (map[string]any, error) {
			if _, err := adminops.Run[map[string]any](ctx, "agent-task-cancel", in.SessionID, owner, in.ID); err != nil {
				return nil, err
			}
			t, err := agents.Cancel(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"task": t, "lease": d.ReleaseTaskLease(ctx, in.SessionID, owner, t)}, nil
		})
		return nil, out, err
	}))
}

type AgentDeps struct {
	Store            *idempotency.Store
	BootTime         func() time.Time
	TaskRead         func(string) (agents.Task, error)
	LeaseHeldBy      func(string, string) bool
	ReleaseTaskLease func(context.Context, string, string, agents.Task) string
}
