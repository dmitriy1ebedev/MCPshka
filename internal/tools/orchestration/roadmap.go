package orchestration

import (
	"cmp"
	"context"
	"errors"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/adminops"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/roadmapitem"
	"mcpshka/internal/roadmaprun"
)

type RoadmapRunInput struct {
	AllowedAgents  []string `json:"allowed_agents" jsonschema:"Only agents explicitly authorized by the owner for this task. Required: codex, claude, or both. Never infer consent from quotas or a general development request."`
	Goal           string   `json:"goal,omitempty" jsonschema:"Arbitrary task (1..3072 UTF-8 bytes). Omit for ROADMAP phases."`
	SessionID      string   `json:"admin_session_id" jsonschema:"Active Face ID window covering this run."`
	Path           string   `json:"path" jsonschema:"Repository to develop (immediate /root/repos/<name>)."`
	MaxPhases      int      `json:"max_phases,omitempty" jsonschema:"Stop after this many phases pass (default 5, max 62)."`
	Hours          int      `json:"hours,omitempty" jsonschema:"Whole-run time budget in hours (default 8, max 24)."`
	IdempotencyKey string   `json:"idempotency_key" jsonschema:"Required retry key."`
}

type RoadmapRunStatusInput struct {
	ID   string `json:"id,omitempty" jsonschema:"Run id from roadmap_run; omit to list recent runs."`
	Path string `json:"path,omitempty" jsonschema:"Optional repository filter for the list."`
}

type RoadmapRunStatusResult struct {
	Run       *roadmaprun.Journal  `json:"run,omitempty"`
	Runs      []roadmaprun.Journal `json:"runs,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
}

func RegisterRoadmap(add func(tooldef.Def), manager *jobs.Manager, d RoadmapDeps) {
	closed, nonDestructive := false, false
	git := gitops.New(manager.Policy())
	add(tooldef.New(&mcp.Tool{
		Name:        "roadmap_next",
		Description: "«Делай пункты roadmap» starts here: the work order for the next unfinished checkbox of any project's ROADMAP, in the phase project_context names — item id, text and its Exit criterion, the phase rules, the stages to done and how to report. An item is done only when roadmap_close marked it [x]; commits are not items. Reads the main checkout's ROADMAP for worktrees.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectProfileInput) (*mcp.CallToolResult, roadmapitem.Order, error) {
		root, err := git.RepoRoot(in.Path)
		if err != nil {
			return nil, roadmapitem.Order{}, err
		}
		file := roadmaprun.MainRoadmap(root)
		n, _ := git.CommitsSince(root, file)
		order, err := roadmapitem.NextFile(root, file, n)
		return nil, order, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "roadmap_close",
		Description: "Close roadmap items: marks the named checkboxes [x] with the date and your note and commits ROADMAP.md under your lease — only when phase_gate phase=<item id> is PASS for every one (clean Git, test of exact HEAD, the item's own checkpoint from phase_checkpoint_record with job evidence for its Exit). A refusal changes nothing and names the missing step. Returns the next work order.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in roadmapitem.CloseInput) (*mcp.CallToolResult, roadmapitem.CloseResult, error) {
		out, err := idempotency.Run(d.Store, in.IdempotencyKey, "roadmap_close", in, func() (roadmapitem.CloseResult, error) {
			root, err := git.RepoRoot(in.Path)
			if err != nil {
				return roadmapitem.CloseResult{}, err
			}
			file := roadmaprun.MainRoadmap(root)
			_, held := d.Leases.ValidateHolder(root, in.LeaseToken)
			return roadmapitem.CloseAccepted(in, file, held, func(id string) string {
				return d.Gate(root, id)
			}, func(msg string) (string, error) {
				if _, tracked := git.CommitsSince(root, file); !tracked {
					return "", nil // an untracked roadmap (Lane, MCPshka) is only edited
				}
				c, err := git.Commit(root, []string{"ROADMAP.md"}, msg)
				return c.Commit, err
			})
		})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "roadmap_run",
		Description: "Start an arbitrary goal+path task (or ROADMAP phases when goal omitted) in MCPshka's own runner for a repository under the active Face ID window. It takes each next ROADMAP item, has Codex or Claude Code implement it with the MCPshka rules (an agent at its usage limit is skipped until its reset) (lease, sealed project_test, phase_gate), checks phase_gate itself, retries once with the blocker and stops on a real blocker, the time budget or max_phases; it waits for a quota reset only when every agent is exhausted. Read the journal with roadmap_run_status in the morning.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in RoadmapRunInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, err := roadmaprun.ParseAllowedAgents("agents:" + strings.Join(in.AllowedAgents, ",")); err != nil {
			return nil, nil, err
		}
		owner, err := adminops.Owner(ctx)
		if err != nil {
			return nil, nil, err
		}
		in.MaxPhases, in.Hours = cmp.Or(in.MaxPhases, 5), cmp.Or(in.Hours, 8)
		args := []string{"roadmap-run", in.SessionID, owner, filepath.Clean(in.Path), strconv.Itoa(in.MaxPhases), strconv.Itoa(in.Hours)}
		if in.Goal != "" {
			goal, err := roadmaprun.EncodeGoal(in.Goal)
			if err != nil {
				return nil, nil, err
			}
			args = append(args, goal)
		}
		args = append(args, "agents:"+strings.Join(in.AllowedAgents, ","))
		out, err := idempotency.Run(d.Store, in.IdempotencyKey, "roadmap_run", in, func() (map[string]any, error) {
			return adminops.Run[map[string]any](ctx, args...)
		})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "roadmap_run_status",
		Description: "Read-only global status: active and latest runs across all projects, goal, current phase, agent, step, last job/gate, blocker and next_action. Without id/path shows all projects; id returns details. Truncated or interrupted evidence never grants ownership.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in RoadmapRunStatusInput) (*mcp.CallToolResult, RoadmapRunStatusResult, error) {
		if in.ID != "" {
			if strings.ContainsAny(in.ID, "/\\") || len(in.ID) > 128 {
				return nil, RoadmapRunStatusResult{}, errors.New("invalid run id")
			}
			j, err := roadmaprun.ReadJournal(filepath.Join(roadmaprun.StateDir, in.ID+".json"), d.BootTime())
			if err != nil {
				return nil, RoadmapRunStatusResult{}, err
			}
			return nil, RoadmapRunStatusResult{Run: &j}, nil
		}
		runs, truncated, err := roadmaprun.Recent(in.Path, d.BootTime())
		return nil, RoadmapRunStatusResult{Runs: runs, Truncated: truncated}, err
	}))
}

type ProjectProfileInput = toolcontract.ProjectProfileInput

type RoadmapDeps struct {
	Store    *idempotency.Store
	Leases   *projectlease.Service
	BootTime func() time.Time
	Gate     func(string, string) string
}
