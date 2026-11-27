package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"mcpshka/internal/evidence"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/projectprofile"
)

func (c *Controller) normalize(p Plan) (Plan, error) {
	if !slices.Contains(Modes, p.Mode) || strings.TrimSpace(p.Goal) == "" || len(p.Goal) > 2048 || len(p.Version) > 128 || len(p.Tier) > 128 {
		return p, errors.New("mission requires a known mode, goal (1..2048 bytes) and bounded version/tier")
	}
	if p.Mode == "TIER" && p.Tier == "" {
		return p, errors.New("TIER requires tier and its explicit step plan")
	}
	root, err := c.git.RepoRoot(p.Path)
	if err != nil {
		return p, err
	}
	p.Path = root
	if p.Deadline != nil {
		if p.Deadline.IsZero() {
			return p, errors.New("nonzero mission deadline required")
		}
		pinned := p.Deadline.UTC()
		p.Deadline = &pinned
	}
	if p.WorkflowPlan != nil {
		pin := *p.WorkflowPlan
		p.WorkflowPlan = &pin
		if p.Mode == "AUDIT_ONLY" || pin.Path != root || p.WorkflowID != "" && p.WorkflowID != pin.ID {
			return p, errors.New("workflow preview path/identity mismatch or audit-only dispatch")
		}
		p.WorkflowID = pin.ID
	}
	if len(p.Steps) == 0 && p.Step != "" {
		p.Steps = []Step{{ID: p.Step}}
	}
	if len(p.Steps) == 0 || len(p.Steps) > 64 || p.Mode == "CLOSE_STEP" && len(p.Steps) != 1 {
		return p, errors.New("mission requires 1..64 steps (CLOSE_STEP: exactly one)")
	}
	if p.WorkflowID != "" && (!workflowID.MatchString(p.WorkflowID) || len(p.Steps) != 1) {
		return p, errors.New("workflow binding requires one scope and a valid original workflow ID")
	}
	if p.WorkflowPlan != nil && (p.WorkflowPlan.Scope != p.Steps[0].ID || p.AgentsAllowed) {
		return p, errors.New("workflow intake requires its exact single scope and native-only execution")
	}
	p.Steps = slices.Clone(p.Steps)
	seen := map[string]bool{}
	for i, step := range p.Steps {
		if strings.TrimSpace(step.ID) == "" || len(step.ID) > 160 || seen[step.ID] {
			return p, errors.New("step ids must be nonempty, unique and at most 160 bytes")
		}
		seen[step.ID] = true
		if len(step.Capabilities) == 0 && p.WorkflowPlan != nil {
			step.Capabilities = []string{"workflow_plan", "workflow_status", "acceptance_status"}
		}
		if len(step.Capabilities) == 0 {
			switch p.Mode {
			case "AUDIT_ONLY":
				step.Capabilities = []string{"project_context", "phase_gate", "acceptance_status"}
			case "DIAGNOSE_AND_RECOVER":
				step.Capabilities = []string{"host_triage", "health_snapshot", "acceptance_status"}
			default:
				step.Capabilities = []string{"project_pipeline", "phase_gate", "acceptance_status", "integration_ship", "release_status"}
			}
		}
		if p.WorkflowID != "" && !slices.Contains(step.Capabilities, "workflow_status") {
			step.Capabilities = append(step.Capabilities, "workflow_status")
		}
		if len(step.Capabilities) > 32 {
			return p, errors.New("at most 32 capabilities per step")
		}
		for _, name := range step.Capabilities {
			if name == "" || len(name) > 64 {
				return p, errors.New("invalid capability name")
			}
		}
		p.Steps[i] = step
	}
	raw, _ := json.Marshal(p)
	if len(raw) > 32<<10 {
		return p, errors.New("mission plan exceeds 32 KiB")
	}
	return p, nil
}

func proven(a Acceptance, now evidence.Inputs) bool {
	if !a.Done || len(a.Gates) == 0 || a.Inputs.Head == "" || a.Inputs.Tree == "" || a.Inputs.Head != now.Head || a.Inputs.Tree != now.Tree || a.Inputs.Version != now.Version {
		return false
	}
	seen := map[string]bool{}
	for _, g := range a.Gates {
		if !slices.Contains(evidence.Gates, g.Gate) || seen[g.Gate] {
			return false
		}
		seen[g.Gate] = true
		// HEAD binds the immutable Git tree even for older Head-only producers.
		if g.State != "PASS" || g.Record == nil || g.Record.Result != "PASS" || g.Record.Inputs.Head != now.Head || g.Record.Inputs.Tree != "" && g.Record.Inputs.Tree != now.Tree {
			return false
		}
	}
	return true
}

func (c *Controller) Preflight(ctx context.Context, p Plan) (Preflight, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	p, err := c.normalize(p)
	if err != nil {
		return Preflight{}, err
	}
	out := Preflight{Plan: p, State: "READY", Steps: []StepCheck{}, Blockers: []Blocker{}}
	add := func(step, reason string, next healthlayer.Action, user string) {
		out.State = "BLOCKED"
		out.Blockers = append(out.Blockers, Blocker{Step: step, Reason: reason, NextSafeAction: next, UserActionRequired: user})
	}
	revision, discoveryErr := c.discover(ctx, p)
	if discoveryErr != nil {
		add("", discoveryErr.Error(), action("capabilities_bootstrap", map[string]any{}, "none"), "none")
		return out, nil
	}
	out.CapabilityRevision = revision
	status, err := c.git.Status(p.Path, 16384)
	if err != nil || status.Dirty || status.Truncated {
		add("", "clean committed HEAD/tree required", action("project_context", map[string]any{"path": p.Path}, "none"), "none")
	}
	head, he := c.git.Revision(p.Path)
	tree, te := c.git.Tree(p.Path)
	out.Inputs = evidence.Inputs{Head: head, Tree: tree, Version: p.Version}
	if he != nil || te != nil || head == "" || tree == "" {
		add("", "Git identity unavailable", action("project_context", map[string]any{"path": p.Path}, "none"), "none")
	}
	if p.WorkflowID != "" {
		observed, err := c.workflowEvidence(ctx, p)
		out.Workflow = observed
		if errors.Is(err, os.ErrNotExist) && p.WorkflowPlan != nil && c.db != nil {
			_, parentErr := c.db.Get("missions/" + workflowMissionID(p.WorkflowID) + ".json")
			if errors.Is(parentErr, os.ErrNotExist) {
				pin := p.WorkflowPlan
				var preview projectprofile.PipelinePin
				err = c.read(ctx, "workflow_plan", map[string]any{"path": p.Path, "pipeline": pin.Pipeline, "id": pin.ID, "roadmap_sha256": pin.RoadmapSHA, "scope": pin.Scope}, &preview)
				if err == nil && preview != *pin {
					err = errors.New("workflow preview changed: HOLD")
				}
			}
		}
		if err != nil {
			add(p.Steps[0].ID, err.Error(), action("workflow_status", map[string]any{"id": p.WorkflowID}, "none"), "none")
		}
	}
	clean := len(out.Blockers) == 0
	for _, step := range p.Steps {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		check := StepCheck{Step: step.ID}
		var cap struct {
			Verdict        string              `json:"verdict"`
			Blocker        string              `json:"blocker"`
			NextSafeAction *healthlayer.Action `json:"next_safe_action"`
			Capabilities   []struct {
				State        string `json:"state"`
				Dependencies []struct {
					Name     string `json:"name"`
					State    string `json:"state"`
					Required bool   `json:"required"`
				} `json:"dependencies"`
			} `json:"capabilities"`
		}
		err := c.read(ctx, "capability_preflight", map[string]any{"step": step.ID, "capabilities": step.Capabilities}, &cap)
		check.CapabilityVerdict = cap.Verdict
		// Only real refusals block a step: a blocked verdict, a REQUIRED
		// dependency that is not ok, an unknown observation or no evidence at
		// all. Optional resource pressure (degraded or stale) is a scheduling
		// signal — the executor enforces its own heavy-queue budget — and a
		// capability without backend dependencies needs only registration.
		// Otherwise stale foreign runs that degrade mission_status would
		// block every new mission forever (owner 08.11: DIAG all-tiers).
		optionalOnly := cap.Verdict != "blocked" && len(cap.Capabilities) > 0
		for _, capability := range cap.Capabilities {
			if !optionalOnly {
				break
			}
			switch {
			case capability.State == "ready":
				continue
			case capability.State == "registered" && len(capability.Dependencies) == 0:
				continue
			case (capability.State == "degraded" || capability.State == "preflight") && len(capability.Dependencies) > 0:
			default:
				optionalOnly = false
				continue
			}
			for _, dep := range capability.Dependencies {
				okState := dep.State == "ok" || dep.State == "ready"
				// An optional "providers" dependency is unknown whenever no
				// agent ran recently; a mission without agents must not stall
				// on it (MCP-166, Lane 1.3.0 BLOCKED on every step 10.11).
				advisory := dep.State == "degraded" || dep.State == "stale" || (dep.Name == "providers" && dep.State == "unknown")
				if !okState && (dep.Required || !advisory) {
					optionalOnly = false
				}
			}
		}
		if err != nil || (cap.Verdict != "ready" && !optionalOnly) {
			reason := cap.Blocker
			if reason == "" {
				reason = "capability readiness not proven: " + cap.Verdict
			}
			if err != nil {
				reason = err.Error()
			}
			next := action("capability_preflight", map[string]any{"step": step.ID, "capabilities": step.Capabilities}, "none")
			if cap.NextSafeAction != nil && cap.NextSafeAction.Tool != "" {
				next = *cap.NextSafeAction
			}
			user := "none"
			if next.Approval != "" && next.Approval != "none" {
				user = next.Approval
			}
			add(step.ID, reason, next, user)
		}
		args := map[string]any{"path": p.Path, "step": step.ID, "version": p.Version}
		if p.Mode == "REPAIR_AND_RELEASE" {
			args["required_gates"] = releaseGates
		}
		if err := c.read(ctx, "acceptance_status", args, &check.Acceptance); err != nil {
			add(step.ID, fmt.Sprintf("acceptance unavailable: %v", err), action("acceptance_status", args, "none"), "none")
		}
		if len(check.Acceptance.Gates) > len(evidence.Gates) {
			return out, errors.New("acceptance gate budget exceeded")
		}
		check.Proven = clean && proven(check.Acceptance, out.Inputs) && (p.WorkflowID == "" || out.Workflow != nil && out.Workflow.State == "done")
		if p.Mode == "REPAIR_AND_RELEASE" && !releaseGatesProven(check.Acceptance.Gates) {
			check.Proven = false
		}
		// Keep gate facts, not repeated full evidence records, in bounded mission state.
		for i := range check.Acceptance.Gates {
			check.Acceptance.Gates[i].Record = nil
		}
		out.Steps = append(out.Steps, check)
	}
	// Detect changes during the observations rather than sealing mixed revisions.
	after, err := c.git.Status(p.Path, 16384)
	lastHead, he := c.git.Revision(p.Path)
	lastTree, te := c.git.Tree(p.Path)
	if err != nil || after.Dirty || after.Truncated || he != nil || te != nil || lastHead != head || lastTree != tree {
		add("", "Git changed during preflight; reconcile before checkpoint", action("project_context", map[string]any{"path": p.Path}, "none"), "none")
		for i := range out.Steps {
			out.Steps[i].Proven = false
		}
	}
	return out, nil
}
