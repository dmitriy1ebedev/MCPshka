package taskplan

import (
	"context"
	"errors"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/capabilities"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP-041/044: a task-oriented layer over the narrow tools. One goal (or
// its steps) becomes one plan: per step the tool, effect, authorization it
// needs, its preview/probe and how the result is verified, plus the total
// Face ID cost and whether anything is missing. Narrow tools stay the
// stable low-level API; nothing is executed here.
type TaskPlanInput struct {
	Goal  string   `json:"goal" jsonschema:"the task in plain words; steps may be joined by «затем», «потом», «;» or «, then»"`
	Steps []string `json:"steps,omitempty" jsonschema:"optional explicit steps (max 12)"`
}

type TaskPlanStep struct {
	NativeCapabilityAvailable bool   `json:"native_capability_available"`
	NativeReadiness           string `json:"native_readiness,omitempty"`
	Goal                      string `json:"goal"`
	Tool                      string `json:"tool,omitempty"`
	Effect                    string `json:"effect,omitempty"`
	Needs                     string `json:"needs,omitempty"` // none, face_id_session, plan_face_id, approval_grant, not_possible
	Preview                   string `json:"preview,omitempty"`
	Verify                    string `json:"verify,omitempty"`
	Missing                   bool   `json:"missing,omitempty"`
	Fallback                  string `json:"fallback,omitempty"`
}

type TaskPlanResult struct {
	NativeCapabilityAvailable bool           `json:"native_capability_available"`
	ExpectedExternalCalls     int            `json:"expected_external_calls"` // shortcut: native plans do not delegate; count remote calls when an external adapter is admitted.
	State                     string         `json:"state"`                   // READY, PARTIAL, MISSING
	Steps                     []TaskPlanStep `json:"steps"`
	FaceIDPrompts             int            `json:"face_id_prompts"`
	ToolCalls                 int            `json:"estimated_tool_calls"`
	Next                      string         `json:"next_action"`
}

// RE2 \b is ASCII-only, so Cyrillic separators are matched by whitespace.
var stepSplit = regexp.MustCompile(`(?i)\s*;\s*|,?\s+(?:затем|потом|а после|then)\s+`)

func Plan(ctx context.Context, in TaskPlanInput, d Deps) (TaskPlanResult, error) {
	steps := in.Steps
	// Do not swallow additional owner clauses joined by "then"/"затем".
	if len(steps) == 0 && len(stepSplit.Split(in.Goal, -1)) == 1 {
		preferred, _ := capabilities.WorkflowRouteForGoal(strings.ToLower(strings.TrimSpace(in.Goal)))
		if len(preferred) > 0 && preferred[0] == "release_prepare" {
			// A release is one guarded workflow, not a single low-level command.
			// Planning these names grants no CI, lease, admin or deploy authority.
			steps = []string{"project_context", "integration_ship", "integration_approve", "integration_ship", "release_prepare", "release_status", "project_deploy", "project_deploy_status", "project_verify"}
		}
	}
	if len(steps) == 0 {
		for _, s := range stepSplit.Split(in.Goal, -1) {
			if s = strings.Trim(s, " ,."); s != "" {
				steps = append(steps, s)
			}
		}
	}
	if len(steps) == 0 || len(steps) > 12 {
		return TaskPlanResult{}, errors.New("task_plan needs 1..12 steps")
	}
	out := TaskPlanResult{State: "READY", NativeCapabilityAvailable: true}
	session, missing := false, 0
	for _, g := range steps {
		st := TaskPlanStep{Goal: g}
		res, err := d.Resolve(ctx, capabilities.ToolResolveInput{Goal: g, Compact: true})
		if err != nil || res.Code == "CAPABILITY_MISSING" || len(res.Candidates) == 0 {
			st.Missing = true
			st.Fallback = res.Instructions
			if st.Fallback == "" {
				st.Fallback = "Record the missing capability with tool_forge action=request. Additional coding agents require the owner's direct permission."
			}
			missing++
			out.Steps = append(out.Steps, st)
			continue
		}
		c := res.Candidates[0]
		st.Tool, st.Effect = c.Name, c.Effect
		st.NativeCapabilityAvailable, st.NativeReadiness = c.PermissionGranted && d.Registered(c.Name), d.Readiness(c.Name)
		out.NativeCapabilityAvailable = out.NativeCapabilityAvailable && st.NativeCapabilityAvailable
		if cov, err := d.Coverage(ctx, c.Name); err == nil {
			st.Needs = cov
			switch cov {
			case "face_id_session":
				session = true
			case "plan_face_id", "approval_grant":
				out.FaceIDPrompts++
			}
		}
		if contract, ok := d.Contract(c.Name); ok {
			st.Preview = "probe " + contract.Probe + " before; retry key " + contract.Key + "; recovery " + contract.Recovery
			st.Verify = "re-run " + contract.Probe + " and compare before/after"
		} else if c.Effect == "read_only" {
			st.Verify = "the result itself (read-only)"
		}
		if res.Code == "CAPABILITY_PARTIAL" {
			st.Fallback = res.Instructions
			if st.Fallback == "" {
				st.Fallback = "Inspect the missing workflow prerequisites before dispatch."
			}
			missing++
		}
		out.ToolCalls += 1 + boolInt(st.Preview != "")*2
		out.Steps = append(out.Steps, st)
	}
	out.NativeCapabilityAvailable = out.NativeCapabilityAvailable && missing == 0
	if session {
		out.FaceIDPrompts++
	}
	switch {
	case missing == len(steps):
		out.State, out.Next = "MISSING", "no step is covered: tool_forge action=request"
	case missing > 0:
		out.State, out.Next = "PARTIAL", "cover the missing steps first (fallback per step); run the covered ones in order"
	default:
		out.Next = "run the steps in order with the listed tools; for a long or resumable chain use workflow_start"
		if session {
			out.Next = "admin_session_prepare → owner Face ID once → " + out.Next
		}
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func Register(add func(tooldef.Def), d Deps) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "task_plan",
		Description: "Plan a whole task (спланировать задачу целиком) from plain words: per step the tool, effect, authorization (none / one Face ID session / per-plan Face ID), preview probe and verification; total Face ID prompts and missing steps with a fallback. Read-only; execute the steps with the listed tools.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in TaskPlanInput) (*mcp.CallToolResult, TaskPlanResult, error) {
		out, err := Plan(ctx, in, d)
		return nil, out, err
	}))
}

type Deps struct {
	Resolve    func(context.Context, capabilities.ToolResolveInput) (capabilities.ToolResolveResult, error)
	Registered func(string) bool
	Readiness  func(string) string
	Coverage   func(context.Context, string) (string, error)
	Contract   func(string) (toolcontract.Contract, bool)
}
