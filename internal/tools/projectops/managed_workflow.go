package projectops

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectjobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/workbenchruntime"
)

func StartManagedWorkflow(manager *jobs.Manager, leases *projectlease.Service, deployments *deploy.Service, profiles *projectprofile.Service, in ProjectRunInput) (ProjectRunResult, error) {
	plan, err := workbenchruntime.WorkflowPlan(profiles, manager.Policy(), in.Path, in.Workflow)
	if err != nil {
		return ProjectRunResult{}, err
	}
	if err := manager.CheckCommand(plan.Command, plan.Args, plan.Root); err != nil {
		return ProjectRunResult{}, err
	}
	pending, err := deployments.Pending()
	if err != nil {
		return ProjectRunResult{}, err
	}
	if pending {
		return ProjectRunResult{}, errors.New("deployment pending: HOLD")
	}
	if _, err := leases.RecoverManaged(plan.Root, manager, deployments); err != nil {
		return ProjectRunResult{}, err
	}
	sum := sha256.Sum256([]byte(in.IdempotencyKey))
	executionID := hex.EncodeToString(sum[:16])
	leased, err := leases.AcquireManaged(plan.Root, executionID, manager)
	if err != nil {
		return ProjectRunResult{}, err
	}
	opts := projectjobs.ProfileOptions(plan, in.TimeoutSeconds, in.OutputLimitBytes)
	opts.ReservedID = "workflow-" + executionID
	opts.PrivateTemp = true
	if opts.MemoryLimitBytes <= 0 || opts.MemoryLimitBytes > 512*1024*1024 {
		opts.MemoryLimitBytes = 512 * 1024 * 1024
	}
	if opts.CPUQuotaPercent <= 0 || opts.CPUQuotaPercent > 50 {
		opts.CPUQuotaPercent = 50
	}
	if opts.ProcessLimit <= 0 || opts.ProcessLimit > 32 {
		opts.ProcessLimit = 32
	}
	if opts.Timeout <= 0 || opts.Timeout > 300*time.Second {
		opts.Timeout = 300 * time.Second
	}
	if opts.OutputLimitBytes <= 0 || opts.OutputLimitBytes > 16384 {
		opts.OutputLimitBytes = 16384
	}
	if opts.CPUTimeLimit <= 0 || opts.CPUTimeLimit > 300*time.Second {
		opts.CPUTimeLimit = 300 * time.Second
	}
	opts.Env = map[string]string{"GOMAXPROCS": "1", "GOMEMLIMIT": "384MiB"}
	job, err := manager.StartWithOptions(plan.Command, plan.Args, plan.Root, opts)
	// Only explicit in-process prelaunch proof permits releasing this own claim.
	// Every possible effect retains its original identity for reconciliation.
	if err != nil {
		if jobs.IsLaunchRejected(err) && job.ID == "" {
			cleanupErr := manager.WithIdleProject(plan.Root, func() error { _, e := leases.Release(plan.Root, leased.Token); return e })
			return ProjectRunResult{}, errors.Join(err, cleanupErr)
		}
		return ProjectRunResult{}, err
	}
	_, _ = leases.RecoverManaged(plan.Root, manager, deployments)
	return ProjectRunResult{Plan: plan, Job: job}, nil
}

func ManagedServices(manager *jobs.Manager) (*projectlease.Service, *deploy.Service) {
	state := filepath.Dir(manager.StateDir())
	return projectlease.New(filepath.Join(state, "leases"), manager.Policy()), deploy.New(manager.Policy(), filepath.Join(state, "deploys"))
}
