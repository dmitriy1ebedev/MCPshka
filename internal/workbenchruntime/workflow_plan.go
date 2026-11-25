package workbenchruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
)

// WorkflowPlan uses an authorized native command first, else the sealed profile.
func WorkflowPlan(profiles *projectprofile.Service, access *policy.Policy, path, name string) (projectprofile.Plan, error) {
	resolved, err := profiles.Resolve(path)
	if err != nil {
		return projectprofile.Plan{}, err
	}
	plan, err := resolved.Plan(name)
	if err != nil || resolved.Source == "builtin" {
		return plan, err
	}
	decision, nativeErr := access.DecideCommandForWorkdir(plan.Command, plan.Args, plan.Root)
	if nativeErr == nil {
		return plan, nil
	}
	if decision.Reason == "explicit denylist" {
		return plan, nativeErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dirty, err := safeGit(ctx, plan.Root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil || dirty != "" {
		rejected := failure.Rejected("WORKFLOW_INPUTS_UNCOMMITTED", "workbenchruntime.WorkflowPlan", "Commit exact workflow inputs before sealed execution.", err)
		rejected.NextSafeAction = failure.Action{Tool: "project_context", Arguments: map[string]any{"path": plan.Root}}
		return plan, rejected
	}
	head, err := safeGit(ctx, plan.Root, "rev-parse", "HEAD")
	if err != nil {
		return plan, err
	}
	plan.Command = WorkflowCommand
	plan.Args = []string{name, projectprofile.ProfileDigest(resolved.Profile), head}
	plan.Fingerprint = ""
	raw, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	sum := sha256.Sum256(raw)
	plan.Fingerprint = hex.EncodeToString(sum[:])
	return plan, nil
}
