package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AdminCoverageInput struct {
	Tool      string         `json:"tool" jsonschema:"tool to check"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema:"optional exact arguments, to run the tool's preflight"`
	SessionID string         `json:"admin_session_id,omitempty" jsonschema:"Face ID session to check against"`
}

type AdminCoverage struct {
	Tool    string `json:"tool"`
	Needs   string `json:"needs"` // none, face_id_session, approval_grant, plan_face_id, not_possible
	Covered bool   `json:"covered"`
	Action  string `json:"action"`
}

// coverage explains what authorization one call needs, so an agent goes from
// an active Face ID session to a runnable operation without guessing.
func (r *toolRegistry) coverage(ctx context.Context, in AdminCoverageInput) (AdminCoverage, error) {
	t := r.lookupTool(in.Tool).tool
	if t == nil {
		return AdminCoverage{}, errors.New("unknown tool " + in.Tool + "; find it with tool_resolve")
	}
	out := AdminCoverage{Tool: in.Tool, Needs: "none", Covered: true, Action: "call it"}
	if pre := toolPreflights[in.Tool]; pre != nil && in.Arguments != nil {
		if err := pre(ctx, in.Arguments); err != nil {
			return AdminCoverage{Tool: in.Tool, Needs: "not_possible", Action: err.Error()}, nil
		}
	}
	if r.lookupTool(in.Tool).readOnly {
		return out, nil
	}
	if ownFaceIDGrant[in.Tool] {
		// By design one plan = one Face ID: an administrative session does
		// not run arbitrary pinned root commands (owner 27.10 saw only
		// "approval_grant" and a wrong mutation_approval_request route).
		out.Needs, out.Covered = "plan_face_id", false
		out.Action = "its own Face ID per pinned plan, not covered by an admin session: approved_task_prepare (or webos_deploy) → give the owner approval_url → server continues the pinned new task after Face ID → approved_task_status. No chat acknowledgement. Expired new approval requires an explicit owner request; the same idempotency_key only replays its original task. Legacy manual tasks may still require approved_task_execute"
		return out, nil
	}
	if c, ok := r.contractFor(in.Tool); ok && c.Irreversible {
		out.Needs, out.Covered = "approval_grant", false
		out.Action = "irreversible: a Face ID session does not cover it; call mutation_approval_request with the exact payload, let the owner grant it, then pass contract_approval_token"
		return out, nil
	}
	raw, _ := json.Marshal(t.InputSchema)
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	json.Unmarshal(raw, &schema)
	if _, takes := schema.Properties["admin_session_id"]; takes {
		out.Needs = "face_id_session"
		out.Covered = in.SessionID != "" && requireActiveAdmin(ctx, in.SessionID) == nil
		out.Action = "pass admin_session_id of the active session"
		if !out.Covered {
			out.Action = "no active session: admin_session_prepare → owner opens the Face ID link → admin_session_begin, then pass its admin_session_id"
		}
	}
	return out, nil
}

func registerAdminCoverage(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "admin_coverage", Description: "Before a protected call: does it need nothing, the active Face ID session (admin_session_id), a separate one-time approval grant (irreversible tools), or is it impossible (preflight, e.g. a container that is not allowlisted)? Returns the exact next action. Read-only; never creates approval requests.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in AdminCoverageInput) (*mcp.CallToolResult, AdminCoverage, error) {
		out, err := r.coverage(ctx, in)
		return nil, out, err
	})
}
