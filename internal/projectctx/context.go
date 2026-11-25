// Package projectctx is the project_context evidence: Git identity, ROADMAP
// position, workflow claim, writer lease, pending jobs and deployments, and
// the next call for each warning. Moved out of mcpserver (0.19 monolith cut).
package projectctx

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/gitops"
	"mcpshka/internal/issues"
	"mcpshka/internal/jobs"
	"mcpshka/internal/project"
	"mcpshka/internal/projectdrift"
	"mcpshka/internal/projectinsights"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/roadmapstyle"
	"mcpshka/internal/workflow"
)

// Drift reads the controller/production drift of a scope; tests replace it.
var Drift = projectdrift.ForScope

// Input is the project_context tool input.
type Input struct {
	ExpectedBranch     string `json:"expected_branch,omitempty"`
	Path               string `json:"path"`
	ExpectedRoadmapSHA string `json:"expected_roadmap_sha256,omitempty"`
}

// Claim is the durable workflow claim on the project.
type Claim struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	Index       int    `json:"index"`
	JobID       string `json:"job_id,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Revision    string `json:"revision"`
}

// Context is the bounded read-only AI handoff of one project.
type Context struct {
	BranchCheck              projectinsights.BranchCheck `json:"branch_check"`
	ProviderLanes            projectinsights.LaneReport  `json:"provider_lanes"`
	ProjectHealth            projectinsights.Report      `json:"project_health"`
	SourceChain              projectdrift.Chain          `json:"source_chain"`
	Drift                    []projectdrift.Flag         `json:"drift,omitempty"`
	Trust                    string                      `json:"trust"`
	CapturedAt               time.Time                   `json:"captured_at"`
	Root                     string                      `json:"root"`
	Revision                 string                      `json:"revision,omitempty"`
	Git                      gitops.StatusResult         `json:"git"`
	Roadmap                  *roadmapscan.Snapshot       `json:"roadmap,omitempty"`
	RoadmapStyle             []string                    `json:"roadmap_style,omitempty"`    // deviations from the shared ROADMAP template and spelling
	RoadmapProgress          *roadmapscan.Analysis       `json:"roadmap_progress,omitempty"` // done/total and stale or contradicting acceptance marks
	CurrentItem              *roadmapscan.Phase          `json:"current_item,omitempty"`
	Claim                    *Claim                      `json:"claim,omitempty"`
	Profile                  string                      `json:"profile,omitempty"`
	ProfileSource            string                      `json:"profile_source,omitempty"`
	Workflows                []string                    `json:"workflows,omitempty"`
	Test                     *project.TestPlan           `json:"test,omitempty"`
	Build                    *project.BuildPlan          `json:"build,omitempty"`
	DeployProfile            string                      `json:"deploy_profile,omitempty"`
	VerifyProfile            string                      `json:"verify_profile,omitempty"`
	Lease                    projectlease.Status         `json:"lease"`
	Jobs                     jobs.ProjectActivity        `json:"jobs"`
	DeploymentPending        bool                        `json:"deployment_pending"`
	DeploymentReconciliation deploy.Reconciliation       `json:"deployment_reconciliation"`
	Warnings                 []string                    `json:"warnings,omitempty"`
	// NextActions turns each warning into the exact next call (MCP-036).
	NextActions []string `json:"next_actions,omitempty"`
}

// NextActions maps warnings to concrete, read-first next steps.
func NextActions(requested string, out Context) []string {
	var next []string
	if requested != "" && filepath.Clean(requested) != out.Root {
		next = append(next, "use the repository root path="+out.Root+" (requested "+filepath.Clean(requested)+")")
	}
	for _, w := range out.Warnings {
		switch w {
		case "reusable_profile_unavailable", "test_strategy_unavailable":
			next = append(next, "project_onboard_preview path="+out.Root+" → review → project_onboard")
		case "git_status_unavailable", "git_revision_unavailable":
			next = append(next, "git_status path="+out.Root+"; on GIT_OWNERSHIP_UNVERIFIED or outside allowed roots: project_repo_preview path="+out.Root)
		case "roadmap_missing":
			next = append(next, "no ROADMAP.md at "+out.Root+": pass an explicit goal (roadmap_run goal=...) or create ROADMAP.md")
		case "dirty_or_incomplete_inspection_requires_reconciliation":
			next = append(next, "git_diff path="+out.Root+" — reconcile unknown work before writing; never reset it")
		case "deployment_reconciliation_hold":
			next = append(next, "project_deploy_reconcile — deployment evidence is HOLD")
		case "branch_mismatch", "claimed_revision_changed":
			next = append(next, "project_lease_status path="+out.Root+"; git_status path="+out.Root+"; git_worktree op=list path="+out.Root+" — сверьте владельца и независимые scopes до записи")
		case "claimed_roadmap_changed":
			next = append(next, "workflow_status for claim "+claimID(out)+" — ROADMAP changed under the claim")
		}
	}
	sort.Strings(next)
	return slices.Compact(next)
}

func claimID(out Context) string {
	if out.Claim == nil {
		return ""
	}
	return out.Claim.ID
}

// A deployment evidence failure must not blind the read-only AI handoff.
// Unknown or broken privileged fence evidence remains a HOLD, never idle.
// ApplyDeploymentReconciliation records deployment evidence; an error is a HOLD.
func ApplyDeploymentReconciliation(out *Context, observed deploy.Reconciliation, reconcileErr error) {
	out.DeploymentReconciliation = observed
	if reconcileErr != nil {
		out.DeploymentReconciliation.Pending = true
		out.DeploymentReconciliation.ReconciliationRequired = true
		out.DeploymentReconciliation.Trust = "UNTRUSTED"
		out.DeploymentPending = true
		out.Warnings = append(out.Warnings, "deployment_reconciliation_hold")
		return
	}
	out.DeploymentPending = observed.Pending
}

// Build reads the evidence. Read-only, never authority to take over, execute
// or mark a phase DONE. version is the running controller (drift check).
func Build(m *jobs.Manager, in Input, issueStore *issues.Store, version string) (Context, error) {
	p := m.Policy()
	projects := project.New(p)
	detected, err := projects.Detect(in.Path)
	if err != nil {
		return Context{}, err
	}
	out := Context{Trust: "UNTRUSTED", CapturedAt: time.Now().UTC(), Root: detected.Root}
	git := gitops.New(p)
	if root, err := git.RepoRoot(out.Root); err == nil {
		out.Root = root
	}
	out.Git, err = git.Status(out.Root, 16384)
	if err != nil {
		out.Warnings = append(out.Warnings, "git_status_unavailable")
	}
	if out.Git.Dirty || out.Git.Truncated {
		out.Warnings = append(out.Warnings, "dirty_or_incomplete_inspection_requires_reconciliation")
	}
	out.Revision, err = git.Revision(out.Root)
	if err != nil {
		out.Warnings = append(out.Warnings, "git_revision_unavailable")
	}
	scan, progress, err := roadmapscan.Analyze(filepath.Join(out.Root, "ROADMAP.md"), in.ExpectedRoadmapSHA)
	if err == nil {
		out.Roadmap = &scan
		out.RoadmapProgress = &progress
		out.CurrentItem = scan.Next
		out.RoadmapStyle = roadmapstyle.CheckFile(out.Root, 10)
	} else if in.ExpectedRoadmapSHA != "" || !errors.Is(err, os.ErrNotExist) {
		return out, err
	} else {
		out.Warnings = append(out.Warnings, "roadmap_missing")
	}
	state := filepath.Dir(m.StateDir())
	out.Lease, err = projectlease.New(filepath.Join(state, "leases"), p).Status(out.Root)
	if err != nil {
		return out, err
	}
	out.Jobs = m.ProjectActivity(out.Root, 16)
	reconciliation, reconcileErr := deploy.New(p, filepath.Join(state, "deploys")).Reconcile()
	ApplyDeploymentReconciliation(&out, reconciliation, reconcileErr)
	claim, err := workflow.New(m).ProjectClaim(out.Root)
	if err != nil {
		return out, err
	}
	if claim != nil {
		out.Claim = &Claim{ID: claim.ID, Scope: claim.Scope, State: claim.State, Index: claim.Index, JobID: claim.JobID, Fingerprint: claim.Fingerprint, Revision: claim.Revision}
		out.CurrentItem = nil
		if out.Roadmap != nil {
			if out.Roadmap.SHA256 != claim.RoadmapSHA {
				out.Warnings = append(out.Warnings, "claimed_roadmap_changed")
			}
			for _, phase := range out.Roadmap.Phases {
				if phase.ID == claim.Scope {
					phase := phase
					out.CurrentItem = &phase
					break
				}
			}
		}
	}
	resolved, err := projectprofile.New(p).Resolve(out.Root)
	if err == nil {
		out.Profile = resolved.Profile.Name
		out.ProfileSource = resolved.Source
		for name := range resolved.Profile.Workflows {
			out.Workflows = append(out.Workflows, name)
		}
		sort.Strings(out.Workflows)
	} else {
		out.Warnings = append(out.Warnings, "reusable_profile_unavailable")
	}
	if plan, err := projects.PlanTest(out.Root); err == nil {
		out.Test = &plan
	} else {
		out.Warnings = append(out.Warnings, "test_strategy_unavailable")
	}
	if plan, err := projects.PlanBuild(out.Root); err == nil {
		out.Build = &plan
	} else {
		out.Warnings = append(out.Warnings, "build_strategy_unavailable")
	}
	if out.Root == deploy.DefaultRepoRoot {
		out.DeployProfile = "mcpshka-native"
		out.VerifyProfile = "mcpshka-native"
	}
	testJob, testPass := "", false
	if j, ok := m.SealedTestEvidence(out.Root, out.Revision); ok {
		testJob = j.ID
		testPass = j.State == jobs.Done && j.ExitCode != nil && *j.ExitCode == 0
	}
	claimedRevision := ""
	if out.Claim != nil {
		claimedRevision = out.Claim.Revision
	}
	out.BranchCheck, err = projectinsights.CheckBranch(out.Git.Branch, in.ExpectedBranch, claimedRevision, out.Revision)
	if err != nil {
		return out, err
	}
	if out.BranchCheck.State == "MISMATCH" {
		out.Warnings = append(out.Warnings, "branch_mismatch")
	}
	if out.BranchCheck.ClaimRevisionState == "MISMATCH" {
		out.Warnings = append(out.Warnings, "claimed_revision_changed")
	}
	out.SourceChain = projectdrift.SourceChain(context.Background(), m.Database(), out.Root, out.Revision, testJob, testPass, out.CapturedAt)
	out.ProviderLanes = projectinsights.ProviderLanes(git, out.Root, out.Git.Branch, out.Git.Upstream, out.Revision)
	out.ProjectHealth = projectinsights.Read(context.Background(), m.Database(), issueStore, out.Root, out.Lease.CommonGitDir, projectinsights.Facts{GitKnown: out.Revision != "" && out.Git.Root != "", Dirty: out.Git.Dirty, Incomplete: out.Git.Truncated || out.Jobs.Truncated || out.DeploymentReconciliation.ReconciliationRequired, UnknownJobs: out.Jobs.UnknownCount, Warnings: out.Warnings})
	out.NextActions = NextActions(in.Path, out)
	common, _ := gitops.CommonDir(out.Root)
	out.Drift = Drift(m.Database(), out.Root, common, version, out.CapturedAt)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 32768 {
		return Context{}, errors.New("project context exceeds 32 KiB evidence budget")
	}
	return out, nil
}
