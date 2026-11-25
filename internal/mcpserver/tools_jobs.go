package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/issues"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/execroute"
	"mcpshka/internal/tools/jobgc"
	"strings"
	"time"
)

func registerJobTools(registry *toolRegistry, d *serverDeps) {
	manager := d.manager
	profiles := d.profiles
	deploys := d.deploys
	leases := d.leases
	pipelines := d.pipelines
	idempotencyStore := d.idempotencyStore
	healthCollector := d.healthCollector
	inbox := d.inbox
	integrations := d.integrations
	closed := false
	destructive := true
	registry.receipts = manager

	addTool(registry, &mcp.Tool{
		Name:        "exec_start",
		Description: "Start one executable without shell interpretation after command-class policy checks. Optional idempotency_key makes transport retries return the original job instead of launching a duplicate. Compatibility: command=mcpshka with args beginning traffic-route is handled in-process through the bounded DIAG root-broker bridge so older cached MCP tool schemas remain usable without granting generic sudo. A client whose tools/list snapshot lacks a tool calls it with command=mcpshka args=[\"tool-invoke\",\"<tool>\",\"<arguments JSON>\"] (or [\"guide\",\"rules\"], [\"resolve\",\"<goal>\"]); read the result with job_output.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ExecStartInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		if err := testRunnerRoute(in.Command, in.Args, in.Workdir); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		if err := execroute.FileList(in.Command, in.Args, in.Workdir); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		if err := execroute.FileContentSearch(in.Command, in.Args, in.Workdir); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		if err := execroute.GitAdd(in.Command, in.Args); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		if err := execroute.JQJSON(in.Command, in.Args, in.Workdir); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		issues.NoteExec(in.Command, in.Args, time.Now())
		// Compatibility routes of `exec_start command=mcpshka <sub> …` for
		// clients with old schemas: one table instead of 17 identical ifs;
		// workdir marks the routes that check the workdir first.
		if self := in.Command == "mcpshka" || in.Command == "/usr/local/bin/mcpshka"; self && len(in.Args) > 0 {
			type compatRoute struct {
				workdir bool
				run     func() (jobs.Snapshot, error)
			}
			routes := map[string]compatRoute{
				"candidate-image":     {false, func() (jobs.Snapshot, error) { return candidatePublishCompat(ctx, manager, leases, in) }},
				"executor-fence":      {false, func() (jobs.Snapshot, error) { return executorFenceCompat(ctx, manager, in) }},
				"executor-canary":     {false, func() (jobs.Snapshot, error) { return executorCanaryCompat(ctx, manager, in) }},
				"health-snapshot":     {false, func() (jobs.Snapshot, error) { return healthSnapshotCompat(ctx, manager, healthCollector, in) }},
				"release-prepare":     {false, func() (jobs.Snapshot, error) { return releasePrepareCompat(ctx, manager, idempotencyStore, in) }},
				"admin-archive":       {false, func() (jobs.Snapshot, error) { return adminArchiveCompat(ctx, manager, in) }},
				"codex-session":       {false, func() (jobs.Snapshot, error) { return codexSessionCompat(ctx, manager, in) }},
				"artifact-upload":     {false, func() (jobs.Snapshot, error) { return artifactUploadCompat(ctx, manager, inbox, in) }},
				"lease-review":        {false, func() (jobs.Snapshot, error) { return leaseReviewCompat(manager, leases, in) }},
				"recovery-diagnose":   {false, func() (jobs.Snapshot, error) { return recoveryDiagnosisCompat(ctx, manager, leases, in) }},
				"work-yield":          {false, func() (jobs.Snapshot, error) { return workYieldCompat(ctx, manager, leases, in) }},
				"lease-heartbeat":     {false, func() (jobs.Snapshot, error) { return leaseHeartbeatCompat(manager, leases, in) }},
				"project-context":     {true, func() (jobs.Snapshot, error) { return projectContextCompat(manager, in) }},
				"integration-worker":  {false, func() (jobs.Snapshot, error) { return integrationWorkerCompat(ctx, manager, in) }},
				"integration-recover": {false, func() (jobs.Snapshot, error) { return integrationRecoveryCompat(ctx, manager, in) }},
				"integration":         {true, func() (jobs.Snapshot, error) { return integrationCompat(ctx, integrations, manager, in) }},
				"pipeline":            {true, func() (jobs.Snapshot, error) { return pipelineCompat(pipelines, manager, in) }},
			}
			if route, ok := routes[in.Args[0]]; ok {
				if route.workdir {
					if _, err := manager.Policy().CheckWorkdir(in.Workdir); err != nil {
						return nil, jobs.Snapshot{}, err
					}
				}
				out, err := route.run()
				return nil, out, err
			}
		}
		if (in.Command == "mcpshka" || in.Command == "/usr/local/bin/mcpshka") && len(in.Args) > 0 && (in.Args[0] == "project-task" || in.Args[0] == "lease-reconcile") {
			if len(in.Env) > 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
				return nil, jobs.Snapshot{}, errors.New("managed recovery requires retry key and no overrides")
			}
			if _, err := manager.Policy().CheckWorkdir(in.Workdir); err != nil {
				return nil, jobs.Snapshot{}, err
			}
			var data any
			if in.Args[0] == "project-task" {
				if len(in.Args) != 3 {
					return nil, jobs.Snapshot{}, errors.New("project-task requires project path and workflow")
				}
				out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, "project-task", in.Args, func() (ProjectRunResult, error) {
					return startManagedWorkflow(manager, leases, deploys, profiles, ProjectRunInput{Path: in.Args[1], Workflow: in.Args[2], Managed: true, IdempotencyKey: in.IdempotencyKey})
				})
				if err != nil {
					return nil, jobs.Snapshot{}, err
				}
				return nil, out.Job, nil
			}
			if len(in.Args) != 2 {
				return nil, jobs.Snapshot{}, errors.New("lease-reconcile requires project path")
			}
			out, err := leases.RecoverManaged(in.Args[1], manager, deploys)
			if err != nil {
				return nil, jobs.Snapshot{}, err
			}
			data = out
			raw, err := json.Marshal(data)
			if err != nil {
				return nil, jobs.Snapshot{}, err
			}
			receipt, err := manager.StoreReceipt(in.Command, in.Args, in.Workdir, string(raw))
			return nil, receipt, err
		}
		if (in.Command == "mcpshka" || in.Command == "/usr/local/bin/mcpshka") && len(in.Args) > 0 && (in.Args[0] == "knowledge" || in.Args[0] == "diag") {
			out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, "knowledge-diag-compat", in.Args, func() (jobs.Snapshot, error) { return knowledgeDIAGCompat(ctx, manager, in) })
			return nil, out, err
		}
		if (in.Command == "mcpshka" || in.Command == "/usr/local/bin/mcpshka") && len(in.Args) > 0 && (in.Args[0] == "jobs-reconcile" || in.Args[0] == "jobs-audit") {
			if len(in.Env) > 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
				return nil, jobs.Snapshot{}, errors.New("control-plane recovery does not accept overrides and requires retry key")
			}
			if _, err := manager.Policy().CheckWorkdir(in.Workdir); err != nil {
				return nil, jobs.Snapshot{}, err
			}
			if in.Args[0] == "jobs-reconcile" {
				if len(in.Args) != 2 {
					return nil, jobs.Snapshot{}, errors.New("jobs-reconcile requires one job id")
				}
				out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, "jobs-reconcile", in.Args, func() (jobs.Snapshot, error) { return manager.Reconcile(in.Args[1]) })
				return nil, out, err
			}
			if len(in.Args) != 1 {
				return nil, jobs.Snapshot{}, errors.New("jobs-audit accepts no arguments")
			}
			data, err := json.Marshal(manager.RegistrySummary(64))
			if err != nil {
				return nil, jobs.Snapshot{}, err
			}
			out, err := manager.StoreReceipt(in.Command, in.Args, in.Workdir, string(data))
			return nil, out, err
		}
		if (in.Command == "mcpshka" || in.Command == "/usr/local/bin/mcpshka") && len(in.Args) > 0 && in.Args[0] == "rootledger-reap" {
			if len(in.Args) != 3 || len(in.Env) > 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
				return nil, jobs.Snapshot{}, errors.New("rootledger-reap requires operation, 16-hex identity and retry key; no environment or approval overrides")
			}
			if _, err := manager.Policy().CheckWorkdir(in.Workdir); err != nil {
				return nil, jobs.Snapshot{}, err
			}
			if !brokerclient.Enabled() {
				return nil, jobs.Snapshot{}, errors.New("rootledger-reap requires PID-bound broker")
			}
			invoke := func() (string, error) {
				data, err := brokerclient.Run(ctx, "rootledger-reap", in.Args[1:]...)
				return string(data), err
			}
			var output string
			var err error
			if in.Args[1] == "status" {
				output, err = invoke()
			} else {
				output, err = runIdempotent(idempotencyStore, in.IdempotencyKey, "rootledger-reap", in.Args, invoke)
			}
			if err != nil {
				return nil, jobs.Snapshot{}, err
			}
			out, err := manager.StoreReceipt(in.Command, in.Args, in.Workdir, output)
			return nil, out, err
		}
		if plan, compat, parseErr := parseTrafficRouteCompat(in.Command, in.Args); compat {
			if parseErr != nil {
				return nil, jobs.Snapshot{}, parseErr
			}
			out, err := runTrafficRouteCompat(ctx, manager, idempotencyStore, in, plan)
			return nil, out, err
		}
		payload := struct {
			Command          string
			Args             []string
			Workdir          string
			TimeoutSeconds   int64
			OutputLimitBytes int64
			MemoryLimitBytes int64
			CPUTimeSeconds   int64
			ProcessLimit     int
			CPUQuotaPercent  int
			PlanHash         string
			Env              map[string]string
			LeaseTokenSHA256 string
		}{in.Command, in.Args, in.Workdir, in.TimeoutSeconds, in.OutputLimitBytes, in.MemoryLimitBytes, in.CPUTimeSeconds, in.ProcessLimit, in.CPUQuotaPercent, in.PlanHash, in.Env, holderTokenDigest(in.LeaseToken)}
		out, err := runIdempotent(idempotencyStore, in.IdempotencyKey, "exec_start", payload, func() (jobs.Snapshot, error) {
			opts := jobs.StartOptions{
				OutputLimitBytes: in.OutputLimitBytes,
				MemoryLimitBytes: in.MemoryLimitBytes,
				ProcessLimit:     in.ProcessLimit,
				CPUQuotaPercent:  in.CPUQuotaPercent,
				ApprovalToken:    in.ApprovalToken,
				PlanHash:         in.PlanHash,
				Env:              in.Env,
				HolderToken:      in.LeaseToken,
			}
			if in.CPUTimeSeconds > 0 {
				opts.CPUTimeLimit = time.Duration(in.CPUTimeSeconds) * time.Second
			}
			if in.TimeoutSeconds > 0 {
				maxSeconds := int64(jobs.MaxJobTimeout / time.Second)
				if in.TimeoutSeconds > maxSeconds {
					opts.Timeout = jobs.MaxJobTimeout
				} else {
					opts.Timeout = time.Duration(in.TimeoutSeconds) * time.Second
				}
			}
			return manager.StartWithOptions(in.Command, in.Args, in.Workdir, opts)
		})
		if err != nil {
			// Clients that know only exec_start keep retrying shell-like
			// commands; name the typed path in every refusal.
			err = fmt.Errorf("%w; %s", err, execStartRefusalHint)
		}
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "job_status",
		Description: "Return compact job state and exit information; verbose=true includes internal identities and default limits.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JobStatusInput) (*mcp.CallToolResult, map[string]any, error) {
		if e := authorizeScopedWorkReceipt(ctx, manager, in.JobID); e != nil {
			return nil, nil, e
		}
		if e := authorizeUploadReceipt(ctx, manager, in.JobID); e != nil {
			return nil, nil, e
		}
		out, err := manager.Get(in.JobID)
		if err != nil {
			return nil, nil, err
		}
		view, err := jobStatusView(out, in.Verbose)
		return nil, view, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "job_wait",
		Description: "Wait up to 45 seconds (larger timeout_seconds are capped to stay inside client call timeouts) for job state change or exit; repeat while running; return compact status and at most 2 KiB of output tails.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JobWaitInput) (*mcp.CallToolResult, JobWaitResult, error) {
		if e := authorizeScopedWorkReceipt(ctx, manager, in.JobID); e != nil {
			return nil, JobWaitResult{}, e
		}
		if e := authorizeUploadReceipt(ctx, manager, in.JobID); e != nil {
			return nil, JobWaitResult{}, e
		}
		out, err := waitJob(ctx, manager, in.JobID, in.TimeoutSeconds)
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "job_output",
		Description: "Read bounded stdout or stderr bytes for a job. Continue from next_cursor; output is never dumped without a server-side cap.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JobOutputInput) (*mcp.CallToolResult, jobs.Output, error) {
		if e := authorizeScopedWorkReceipt(ctx, manager, in.JobID); e != nil {
			return nil, jobs.Output{}, e
		}
		if e := authorizeUploadReceipt(ctx, manager, in.JobID); e != nil {
			return nil, jobs.Output{}, e
		}
		out, err := manager.Read(in.JobID, in.Stream, in.Cursor, in.Limit)
		return nil, out, err
	})

	addTool(registry, &mcp.Tool{
		Name:        "job_cancel",
		Description: "Cancel a running job by signalling its entire process group; escalates from TERM to KILL after one second if necessary.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JobIDInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		if err := authorizeScopedWorkReceipt(ctx, manager, in.JobID); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		out, err := manager.Cancel(in.JobID)
		return nil, out, err
	})
	jobgc.Register(registry.install, manager, registry.store, requireActiveAdmin)
}

// testRunnerRoute preserves compatibility for legacy MCP tests and callers;
// the bounded admission policy lives in its own execroute module.
func testRunnerRoute(command string, args []string, workdir string) error {
	return execroute.TestRunnerRoute(command, args, workdir)
}
