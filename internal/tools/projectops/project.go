package projectops

import (
	"context"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/budget"
	"mcpshka/internal/deploy"
	"mcpshka/internal/evidence"
	"mcpshka/internal/forgeci"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/project"
	"mcpshka/internal/projectjobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/repopath"
	"mcpshka/internal/runnerhub"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/verify"
	"mcpshka/internal/workbenchruntime"
	"path/filepath"
	"strings"
)

func Register(add func(tooldef.Def), d Deps) {
	registerArtifactCleanup(add, d)
	manager := d.Manager
	git := d.Git
	projects := d.Projects
	profiles := d.Profiles
	deploys := d.Deploys
	verifier := d.Verifier
	leases := d.Leases
	idempotencyStore := d.Store
	closed := false
	destructive := true
	nonDestructive := false

	add(tooldef.New(&mcp.Tool{
		Name:        "git_status",
		Description: "Return bounded structured Git status for a repository under an allowed root, without invoking a shell.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in GitStatusInput) (*mcp.CallToolResult, gitops.StatusResult, error) {
		out, err := git.Status(in.Path, in.Limit)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "git_diff",
		Description: "Return a bounded working-tree or staged Git diff. External diff drivers and textconv are disabled.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in GitDiffInput) (*mcp.CallToolResult, gitops.DiffResult, error) {
		out, err := git.Diff(in.Path, in.Cached, in.ContextLines, in.Limit)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "git_log",
		Description: "git log / git rev-parse without a shell: the commit a ref (default HEAD; branch, tag, HEAD~n, sha) resolves to and its newest commits (limit ≤ 50) as «sha ISO-date subject». Remote state: git_provider_query.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in GitLogInput) (*mcp.CallToolResult, gitops.LogResult, error) {
		out, err := git.Log(in.Path, in.Ref, in.Limit)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_detect",
		Description: "Identify a project root, Git state and common language/build markers without executing project code.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectDetectInput) (*mcp.CallToolResult, project.Result, error) {
		out, err := projects.Detect(in.Path)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_profile",
		Description: "Resolve a reusable bounded workflow profile for a project. Repository-local .mcpshka/profile.json overrides built-in profiles after strict validation.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectProfileInput) (*mcp.CallToolResult, projectprofile.Resolution, error) {
		out, err := profiles.Resolve(in.Path)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_onboard_preview",
		Description: "Draft a reusable workflow profile for any repository from its markers (Make/Go/Python/Node). Read-only; review and override, then register with project_onboard.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectProfileInput) (*mcp.CallToolResult, ProjectOnboardPreview, error) {
		out := ProjectOnboardPreview{}
		if current, err := profiles.Resolve(in.Path); err == nil {
			out.Current = &current
		}
		proposed, err := profiles.Propose(in.Path)
		if err != nil {
			out.ProposalError = err.Error()
		} else {
			out.Proposed = &proposed
		}
		return nil, out, nil
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_onboard",
		Description: "Register (or replace) the reviewed workflow profile of any repository under /root/repos, shared by all its worktrees. project_test/project_run then use its test workflow; a registered test runs sealed: every tracked file of the committed HEAD, offline, read-only, bounded. Without profile the drafted proposal is registered.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectOnboardInput) (*mcp.CallToolResult, projectprofile.Resolution, error) {
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "project_onboard", in, func() (projectprofile.Resolution, error) {
			profile := in.Profile
			if profile == nil {
				proposed, err := profiles.Propose(in.Path)
				if err != nil {
					return projectprofile.Resolution{}, err
				}
				profile = &proposed
			}
			return profiles.Register(in.Path, *profile)
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "workflow_checkpoint_start",
		Description: "Create a durable P7 workflow checkpoint from a policy-resolved named project profile plan, pinned by SHA-256. Duplicate starts with the same workflow identity and plan are safe; a changed plan is rejected. This does not execute any step.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in WorkflowCheckpointStartInput) (*mcp.CallToolResult, projectprofile.Checkpoint, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, projectprofile.Checkpoint{}, errors.New("idempotency_key required")
		}
		payload := struct{ WorkflowID, Path, Workflow string }{in.WorkflowID, in.Path, in.Workflow}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "workflow_checkpoint_start", payload, func() (projectprofile.Checkpoint, error) {
			db := manager.Database()
			if db == nil {
				return projectprofile.Checkpoint{}, errors.New("durable SQLite authority unavailable")
			}
			plan, err := workbenchruntime.WorkflowPlan(profiles, manager.Policy(), in.Path, in.Workflow)
			if err != nil {
				return projectprofile.Checkpoint{}, err
			}
			checkpoint, _, err := projectprofile.StartCheckpoint(db, in.WorkflowID, plan.Fingerprint)
			return checkpoint, err
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "workflow_checkpoint_status",
		Description: "Read one durable P7 workflow step checkpoint from the SQLite authority. A missing or invalid checkpoint fails closed; this read-only tool never claims or replays a step.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in WorkflowCheckpointInput) (*mcp.CallToolResult, projectprofile.Checkpoint, error) {
		db := manager.Database()
		if db == nil {
			return nil, projectprofile.Checkpoint{}, errors.New("durable SQLite authority unavailable")
		}
		out, err := projectprofile.LoadCheckpoint(db, in.WorkflowID)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_run",
		Description: "Run one named workflow from the resolved reusable project profile as a normal bounded MCPshka job. Request limits may only tighten, never raise, profile limits.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectRunInput) (*mcp.CallToolResult, ProjectRunResult, error) {
		payload := struct {
			Path             string
			Workflow         string
			TimeoutSeconds   int64
			OutputLimitBytes int64
			Managed          bool
		}{in.Path, in.Workflow, in.TimeoutSeconds, in.OutputLimitBytes, in.Managed}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "project_run", payload, func() (ProjectRunResult, error) {
			if c := profiles.CIFor(in.Path); c != nil && c.ExternalOnly && (in.Workflow == "test" || in.Workflow == "build" || in.Workflow == "package" || in.Workflow == "check" || in.Workflow == "lint") {
				return ProjectRunResult{}, fmt.Errorf("external-only CI profile: forge_ci provider=distributed mode=wait path=%s commit=<HEAD>; H96 only installs verified artifacts", in.Path)
			}
			if in.Managed {
				return StartManagedWorkflow(manager, leases, deploys, profiles, in)
			}
			plan, err := profiles.Plan(in.Path, in.Workflow)
			if err != nil {
				return ProjectRunResult{}, err
			}
			// MCP-188: host jobs follow the general command policy, which
			// keeps interpreters such as python3 off the host. A registered
			// profile stage runs in the sealed executor instead; name that
			// route rather than a bare POLICY_DENIED (14.11: go2rtc package).
			if decision, perr := manager.Policy().DecideCommandForWorkdir(plan.Command, plan.Args, plan.Root); perr != nil && !decision.Allowed {
				// MCP-216: a named, registered browser test is a sealed profile
				// workflow, not permission for unrestricted host node. Route
				// it to the same root-owned, pinned Workbench named workflow
				// as managed=true; never route explicit denylist commands.
				if sealedBrowserFallback(plan, decision.Reason) {
					return StartManagedWorkflow(manager, leases, deploys, profiles, in)
				}
				if projectprofile.ValidStage(in.Workflow) {
					return ProjectRunResult{Plan: plan}, fmt.Errorf("%w; next_safe_action: project_pipeline path=%s stages=[%s] — the same registered profile command runs in the sealed executor on the committed HEAD (commit first; artifacts land in the pipeline store and webos_deploy/installers take them by the same path)", perr, plan.Root, in.Workflow)
				}
			}
			opts := projectjobs.ProfileOptions(plan, in.TimeoutSeconds, in.OutputLimitBytes)
			job, err := manager.StartWithOptions(plan.Command, plan.Args, plan.Root, opts)
			return ProjectRunResult{Plan: plan, Job: job}, err
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "started"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "project_run", Workdir: in.Path, Result: result,
				Details: map[string]any{"workflow": in.Workflow, "profile": out.Plan.Profile},
			})
		}
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_test",
		Description: "Choose a bounded test strategy from project markers and start it as a normal MCPshka job without shell interpretation. packages/run (targeted) run on the working tree, uncommitted changes included. Without them an onboarded repository runs its sealed profile test on the exact committed HEAD: commit first with project_commit (no raw git needed), then test.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectTestInput) (*mcp.CallToolResult, ProjectTestResult, error) {
		payload := struct {
			Packages         []string
			Run              string
			Path             string
			TimeoutSeconds   int64
			OutputLimitBytes int64
			LeaseTokenSHA256 string
			Local            bool
		}{in.Packages, in.Run, in.Path, in.TimeoutSeconds, in.OutputLimitBytes, d.Digest(in.LeaseToken), in.Local}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "project_test", payload, func() (ProjectTestResult, error) {
			if c := profiles.CIFor(in.Path); c != nil && c.ExternalOnly && !in.Local {
				job, root, err := distributedProjectJob(manager, git, in.Path, in.IdempotencyKey, in.LeaseToken)
				return ProjectTestResult{Plan: project.TestPlan{Root: root, Kind: "distributed", Command: forgeci.Command, Reason: "profile requires four-site CI; H96 install only"}, Job: job}, err
			}
			plan, err := projects.PlanTargetedTest(in.Path, in.Packages, in.Run)
			if len(in.Packages) == 0 && in.Run == "" {
				// An onboarded repository's own test workflow wins over marker guesses.
				if p, perr := profiles.Plan(in.Path, "test"); perr == nil && p.Source != "builtin" {
					plan, err = project.TestPlan{Root: p.Root, Kind: "profile", Command: p.Command, Args: p.Args, Reason: p.Source + " profile " + p.Profile + " test workflow; обязательные sealed gates сохранены; " + plan.Reason}, nil
					// Permit nested agent worktrees only when their .git link is
					// anchored in a real immediate repository, never arbitrary paths.
					// The Workbench runtime also checks the same identity before
					// planning and decoding a sealed executor binding.
					if filepath.Dir(p.Root) != "/root/repos" && repopath.Worktree(p.Root) == "" {
						return ProjectTestResult{}, fmt.Errorf("sealed profile test needs a verified repository or linked worktree, got %s: next_action: use an existing native project_worktree and verify its Git identity", p.Root)
					}
				}
			}
			if err != nil {
				return ProjectTestResult{}, err
			}
			timeout := in.TimeoutSeconds
			// A budget shorter than the last passing run only produces a timeout
			// (DIAG P03: 300 s for a ~5 min suite); raise it with a margin.
			if last, ok := manager.LastSealedSuccess(plan.Root); ok && timeout > 0 {
				need := min(int64(last.Seconds()*1.5)+60, budget.CeilingSeconds)
				if timeout < need {
					plan.Reason += fmt.Sprintf("; timeout raised %ds→%ds (last passing run %ds)", timeout, need, int64(last.Seconds()))
					timeout = need
				}
			}
			plan.Args = project.GoTestNoTimeout(plan)
			opts := projectjobs.Options(timeout, in.OutputLimitBytes)
			plan.Reason += "; " + manager.Budget(plan.Command, plan.Args, plan.Root, opts.Timeout).String()
			opts.LowPriority = true
			opts.HolderToken = in.LeaseToken
			opts.RequireExternal = plan.Kind == "profile"
			if plan.Kind == "go" {
				opts.PrivateTemp = true
				// Linking large test binaries alongside the go command peaks just
				// over 1 GiB on H96; a full suite also needs more than 30m CPU.
				opts.Env = map[string]string{"GOMAXPROCS": "2", "GOMEMLIMIT": "640MiB"}
				opts.MemoryLimitBytes = projectjobs.TestMemoryLimit
				opts.CPUTimeLimit = projectjobs.TestCPUTimeLimit
			}
			startLocal := func() (jobs.Snapshot, error) {
				return manager.StartWithOptions(plan.Command, plan.Args, plan.Root, opts)
			}
			if plan.Kind == "go" && len(in.Packages) > 0 && in.Run == "" && !in.Local {
				if job, ok := OffboxTest(ctx, &plan, in.Packages, int(timeout), startLocal); ok {
					return ProjectTestResult{Plan: plan, Job: job}, nil
				}
			}
			job, err := startLocal()
			// 0.9.9: a busy H96 names the off-box route instead of letting the
			// chat wait in the queue; phase_gate accepts a runner's full go test.
			if err == nil && job.State == jobs.Queued && plan.Kind == "profile" && runnerhub.Default.Fit("") == "" {
				plan.Reason += "; H96 queue is busy and a live runner is free: runner_task path=" + plan.Root + " commit=<HEAD> profile=go-test (Go projects) runs the same HEAD off-box now (keep this job; phase_gate accepts whichever passes first)"
			}
			return ProjectTestResult{Plan: plan, Job: job}, err
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_build",
		Description: "Choose a safe marker-based build strategy and start it as a bounded MCPshka job without shell interpretation.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectBuildInput) (*mcp.CallToolResult, ProjectBuildResult, error) {
		payload := struct {
			Path             string
			TimeoutSeconds   int64
			OutputLimitBytes int64
			LeaseTokenSHA256 string
		}{in.Path, in.TimeoutSeconds, in.OutputLimitBytes, d.Digest(in.LeaseToken)}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "project_build", payload, func() (ProjectBuildResult, error) {
			if c := profiles.CIFor(in.Path); c != nil && c.ExternalOnly {
				job, root, err := distributedProjectJob(manager, git, in.Path, in.IdempotencyKey, in.LeaseToken)
				return ProjectBuildResult{Plan: project.BuildPlan{Root: root, Kind: "distributed", Command: forgeci.Command, Reason: "profile requires remote build and SHA-256 artifacts"}, Job: job}, err
			}
			plan, err := projects.PlanBuild(in.Path)
			// The profile's build workflow wins over marker guesses: DIAG's
			// Makefile made it `make` (privileged) and blocked P11 (BUG-20261023-69).
			if p, perr := profiles.Plan(in.Path, "build"); perr == nil {
				plan, err = project.BuildPlan{Root: p.Root, Kind: "profile", Command: p.Command, Args: p.Args, Reason: p.Source + " profile " + p.Profile + " build workflow"}, nil
			}
			if err != nil {
				return ProjectBuildResult{}, err
			}
			opts := projectjobs.Options(in.TimeoutSeconds, in.OutputLimitBytes)
			opts.LowPriority = true
			opts.HolderToken = in.LeaseToken
			if plan.Command == "go" {
				// The Go compiler/linker now exceeds 1 GiB on H96; use the test envelope.
				opts.MemoryLimitBytes = projectjobs.TestMemoryLimit
				opts.PrivateTemp = true
			}
			job, err := manager.StartWithOptions(plan.Command, plan.Args, plan.Root, opts)
			return ProjectBuildResult{Plan: plan, Job: job}, err
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_verify",
		Description: "Verify the deployed MCPshka project against an expected production version. Checks Git cleanliness, installed and rollback binaries, MCPshka and tunnel services, health/ready endpoints, DNS, and outbound HTTPS with bounded results.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectVerifyInput) (*mcp.CallToolResult, verify.Result, error) {
		out, err := verifier.Verify(ctx, in.Path, in.ExpectedVersion)
		if err == nil {
			if in.DeploymentID != "" {
				d.RecordProductionSource(ctx, req, in, &out)
			} else {
				d.RecordGate(in.Path, "deploy", out.OK, evidence.Inputs{Version: in.ExpectedVersion}, "project_verify", "")
			}
		}
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "project_cleanup_candidates",
		Description: "Snapshot bounded candidate content and actual production/rollback identities (controller and broker). A candidate is removed only when MCPshka's own release receipt (release-plan / coordinated-release JSON) names its exact content for a release older than the rollback; everything else — unreceipted, unreadable, current or newer — is kept as UNKNOWN. Filenames never authorize deletion. Apply requires the exact fingerprint and idempotency.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectCandidateCleanupInput) (*mcp.CallToolResult, deploy.CandidateCleanup, error) {
		leaseView, err := leases.Status(in.Path)
		if err != nil {
			return nil, deploy.CandidateCleanup{}, err
		}
		in.Path = leaseView.ProjectRoot
		cleanup := func(fingerprint string) (deploy.CandidateCleanup, error) {
			if in.CandidateID != "" {
				return deploys.CleanupOwnedCandidate(ctx, in.Path, in.CandidateID, fingerprint)
			}
			return deploys.CleanupCandidates(ctx, in.Path, fingerprint)
		}
		if strings.TrimSpace(in.ExpectedFingerprint) == "" {
			out, err := cleanup("")
			return nil, out, err
		}
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, deploy.CandidateCleanup{}, errors.New("idempotency_key is required when applying project_cleanup_candidates")
		}
		payload := struct {
			Path        string
			Fingerprint string
			CandidateID string
		}{in.Path, in.ExpectedFingerprint, in.CandidateID}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "project_cleanup_candidates", payload, func() (deploy.CandidateCleanup, error) {
			var out deploy.CandidateCleanup
			err := CleanupAdmission(manager, leases, in.Path, func() error { var e error; out, e = cleanup(in.ExpectedFingerprint); return e })
			return out, err
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "applied"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "project_cleanup_candidates", Workdir: in.Path, Result: result,
				Details: map[string]any{"removed": len(out.Remove), "removed_bytes": out.RemoveBytes},
			})
		}
		return nil, out, err
	}))
}

type GitStatusInput = toolcontract.GitStatusInput
type GitDiffInput = toolcontract.GitDiffInput

type GitLogInput struct {
	Path  string `json:"path" jsonschema:"repository or worktree under an allowed root"`
	Ref   string `json:"ref,omitempty" jsonschema:"HEAD (default), branch, tag, HEAD~n or commit"`
	Limit int    `json:"limit,omitempty" jsonschema:"commits, 1..50 (default 1)"`
}
type ProjectDetectInput = toolcontract.ProjectDetectInput
type ProjectProfileInput = toolcontract.ProjectProfileInput
type ProjectOnboardPreview = toolcontract.ProjectOnboardPreview
type ProjectOnboardInput = toolcontract.ProjectOnboardInput
type WorkflowCheckpointStartInput = toolcontract.WorkflowCheckpointStartInput
type WorkflowCheckpointInput = toolcontract.WorkflowCheckpointInput
type ProjectRunInput = toolcontract.ProjectRunInput
type ProjectRunResult = toolcontract.ProjectRunResult
type ProjectTestInput = toolcontract.ProjectTestInput
type ProjectTestResult = toolcontract.ProjectTestResult
type ProjectBuildInput = toolcontract.ProjectBuildInput
type ProjectBuildResult = toolcontract.ProjectBuildResult
type ProjectVerifyInput = toolcontract.ProjectVerifyInput
type ProjectCandidateCleanupInput = toolcontract.ProjectCandidateCleanupInput
type Deps struct {
	Manager                *jobs.Manager
	Git                    *gitops.Service
	Projects               *project.Service
	Profiles               *projectprofile.Service
	Deploys                *deploy.Service
	Verifier               *verify.Service
	Leases                 *projectlease.Service
	Store                  *idempotency.Store
	Digest                 func(string) string
	RecordProductionSource func(context.Context, *mcp.CallToolRequest, ProjectVerifyInput, *verify.Result)
	RecordGate             func(string, string, bool, evidence.Inputs, string, string)
}

func distributedProjectJob(manager *jobs.Manager, git *gitops.Service, path, key, token string) (jobs.Snapshot, string, error) {
	s, e := git.Status(path, 4096)
	if e != nil {
		return jobs.Snapshot{}, path, e
	}
	if s.TrackedDirty || s.Truncated {
		return jobs.Snapshot{}, s.Root, errors.New("distributed CI requires committed HEAD")
	}
	head, e := git.Revision(s.Root)
	if e != nil {
		return jobs.Snapshot{}, s.Root, e
	}
	args, opts, e := forgeci.JobInput("distributed", "wait", head, key, token, s.Root)
	if e != nil {
		return jobs.Snapshot{}, s.Root, e
	}
	j, e := manager.StartWithOptions(forgeci.Command, args, s.Root, opts)
	return j, s.Root, e
}
