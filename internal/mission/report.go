package mission

import (
	"context"
	"fmt"
	"mcpshka/internal/evidence"
	"strings"
	"time"
)

type Summary struct {
	Mission                string   `json:"mission"`
	CurrentStep            string   `json:"current_step"`
	State                  string   `json:"state"`
	Version                string   `json:"version"`
	GitHEAD                string   `json:"git_head"`
	PassedGates            []string `json:"passed_gates"`
	Pending                []string `json:"pending"`
	Production             string   `json:"production"`
	ImplementationComplete string   `json:"implementation_complete"`
	Integrated             string   `json:"integrated"`
	ProductionVerified     string   `json:"production_verified"`
	Accepted               string   `json:"accepted"`
	ActiveJob              string   `json:"active_job"`
	Holder                 string   `json:"holder"`
	RecentCommits          []string `json:"recent_commits,omitempty"`
	NextAction             string   `json:"next_action"`
	UserActionRequired     string   `json:"user_action_required"`
	Text                   string   `json:"text"`
}

func (c *Controller) Report(ctx context.Context, id string) (Summary, error) {
	s, err := c.Status(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{Mission: s.ID, State: s.State, Version: s.Version, GitHEAD: s.Inputs.Head, Production: "UNKNOWN", ImplementationComplete: "UNKNOWN", Integrated: "UNKNOWN", ProductionVerified: "UNKNOWN", Accepted: "UNKNOWN", ActiveJob: "UNKNOWN", NextAction: s.NextSafeAction.Tool, UserActionRequired: s.UserActionRequired, PassedGates: []string{}, Pending: []string{}}
	if out.Version == "" {
		out.Version = "UNKNOWN"
	}
	if out.GitHEAD == "" {
		out.GitHEAD = "UNKNOWN"
	}
	if out.UserActionRequired == "" {
		out.UserActionRequired = "none"
	}
	if s.Current < len(s.Steps) {
		out.CurrentStep = s.Steps[s.Current].ID
		if s.Current >= len(s.Checks) || !s.Checks[s.Current].Proven {
			out.Pending = append(out.Pending, "acceptance not proven at current HEAD/tree")
		}
		var gates []evidence.GateState
		if s.Current < len(s.Checks) {
			gates = s.Checks[s.Current].Acceptance.Gates
		}
		for _, g := range gates {
			if g.State == "PASS" {
				out.PassedGates = append(out.PassedGates, g.Gate)
			} else {
				out.Pending = append(out.Pending, g.Gate+": "+g.State)
			}
		}
	} else {
		out.CurrentStep = "complete"
		if s.LastProven != nil {
			out.PassedGates = append(out.PassedGates, s.LastProven.Gates...)
		}
	}
	// Only revalidated, pinned checkpoints qualify as proof. A CI PASS or a
	// nominal DONE label must never imply integration or production delivery.
	for i, checkpoint := range s.Checkpoints {
		if i >= len(s.Checks) || !s.Checks[i].Proven || checkpoint.Step != s.Checks[i].Step || checkpoint.Inputs != s.Checks[i].Acceptance.Inputs {
			continue
		}
		for _, gate := range checkpoint.Gates {
			switch gate {
			case "engineering":
				out.ImplementationComplete = "VERIFIED"
			case "integration":
				out.Integrated = "VERIFIED"
			case "deploy":
				out.ProductionVerified = "VERIFIED"
			}
		}
	}
	if s.State == "DONE" && len(s.Checkpoints) == len(s.Steps) && len(s.Steps) > 0 &&
		out.ImplementationComplete == "VERIFIED" && out.Integrated == "VERIFIED" && out.ProductionVerified == "VERIFIED" {
		out.Accepted = "VERIFIED"
	}
	for _, b := range s.Blockers {
		out.Pending = append(out.Pending, b.Reason)
	}
	var project struct {
		DeploymentPending bool `json:"deployment_pending"`
		Jobs              *struct {
			Jobs []struct {
				ID string `json:"id"`
			} `json:"jobs"`
			Truncated    bool `json:"truncated"`
			UnknownCount int  `json:"unknown_count"`
		} `json:"jobs"`
	}
	if c.read(ctx, "project_context", map[string]any{"path": s.Path}, &project) == nil {
		if project.DeploymentPending {
			out.Production = "PENDING; project_deploy_status"
		}
		if project.Jobs != nil && !project.Jobs.Truncated && project.Jobs.UnknownCount == 0 {
			out.ActiveJob = "none"
			ids := []string{}
			for _, job := range project.Jobs.Jobs {
				ids = append(ids, job.ID)
			}
			if len(ids) > 0 {
				out.ActiveJob = strings.Join(ids, ", ")
			}
		}
	}
	// Who is driving the project now and what landed lately: two chats (the
	// main session and an hourly task) must report the same picture (08.11).
	out.Holder = "UNKNOWN"
	var lease struct {
		Held        bool      `json:"held"`
		Owner       string    `json:"owner"`
		HeartbeatAt time.Time `json:"heartbeat_at"`
		Activity    struct {
			Count int `json:"count"`
		} `json:"activity"`
	}
	if c.read(ctx, "project_lease_status", map[string]any{"path": s.Path}, &lease) == nil {
		out.Holder = "free"
		if lease.Held {
			out.Holder = lease.Owner
			if out.Holder == "" {
				out.Holder = "held (owner not named)"
			}
			// A silent holder with no jobs is not a reason to re-check every
			// hour (Lane 08.11: 8 h of identical ticks): name the one owner
			// step that ends it (MCP-155).
			if !lease.HeartbeatAt.IsZero() && time.Since(lease.HeartbeatAt) > 30*time.Minute && lease.Activity.Count == 0 {
				out.Holder += fmt.Sprintf(" (silent %s, no jobs)", time.Since(lease.HeartbeatAt).Round(time.Minute))
				out.UserActionRequired = "owner Face ID admin session, then project_lease_admin_stale_preview + project_lease_admin_stale_release path=" + s.Path + "; do not repeat identical ticks meanwhile"
			}
		}
	}
	if c.git != nil {
		out.RecentCommits, _ = c.git.RecentCommits(s.Path, 5)
	}
	out.Text = fmt.Sprintf("Mission · %s\nCurrent step · %s\nState · %s\nVersion · %s\nGit HEAD · %s\nPassed gates · %s\nPending · %s\nImplementation complete · %s\nIntegrated · %s\nProduction verified · %s\nAccepted · %s\nProduction · %s\nActive job · %s\nNext action · %s\nUser action required · %s\nProject held by · %s\nRecent commits · %s", out.Mission, out.CurrentStep, out.State, out.Version, out.GitHEAD, strings.Join(out.PassedGates, ", "), strings.Join(out.Pending, "; "), out.ImplementationComplete, out.Integrated, out.ProductionVerified, out.Accepted, out.Production, out.ActiveJob, out.NextAction, out.UserActionRequired, out.Holder, strings.Join(out.RecentCommits, " | "))
	return out, nil
}
