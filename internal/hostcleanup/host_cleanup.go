package hostcleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"mcpshka/internal/gitprovider"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/releaseapply"
	"os"
	"os/exec"
	"time"
)

// PID 1 owns each bounded maintenance worker. Broker restart after apply
// schedules an immediate pass; a busy deployment lock postpones deletion.
func Run(ctx context.Context, report func(error)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		child, cancel := context.WithTimeout(ctx, 5*time.Second)
		args := []string{"--unit=mcpshka-host-cleanup", "--collect", "--no-block", "--quiet",
			"--property=Type=exec", "--property=User=root", "--property=Group=mcpshka", "--property=UMask=0007",
			"--property=RuntimeMaxSec=90s", "--property=MemoryMax=256M", "--property=CPUQuota=25%", "--property=TasksMax=16",
			"--property=Nice=19", "--property=IOSchedulingClass=idle", "--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6",
			"--property=NoNewPrivileges=yes", "--property=ProtectSystem=strict", "--property=PrivateTmp=yes",
			"--property=ReadWritePaths=/root /var/lib/mcpshka /var/lib/mcpshka-executor /run/mcpshka-self-deploy.lock",
			Helper, "host-cleanup-worker"}
		cmd := exec.CommandContext(child, "/usr/bin/systemd-run", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		if out, err := cmd.CombinedOutput(); err != nil && ctx.Err() == nil && report != nil {
			report(fmt.Errorf("host cleanup scheduling: %w: %.512s", err, out))
		}
		cancel()
		timer.Reset(time.Hour)
	}
}
func RunWorker() ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, executor.Hold(failure.PolicyDenied, "HOST_CLEANUP_ROOT_REQUIRED", "rootbroker.RunWorker", "host cleanup worker requires root", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	return runHostCleanupPass(ctx, executor.UpdateHostStorage, func() bool { ready, _ := heavyqueue.HostReady(); return ready },
		func(ctx context.Context) (executor.ReleaseCleanupPlan, error) {
			plan, err := executor.MaintainReleaseStorage(ctx, true)
			if ctx.Err() == nil {
				err = errors.Join(err, gitprovider.PruneDistributedRefs(ctx, time.Now()))
			}
			return plan, err
		},
		func(raw []byte) error {
			return releaseapply.WriteRootFile(executor.AuthorityRoot+"/cleanup-last-plan.json", raw, 0600)
		})
}

func runHostCleanupPass(ctx context.Context, sample func(context.Context) error, ready func() bool,
	cleanup func(context.Context) (executor.ReleaseCleanupPlan, error), save func([]byte) error) ([]byte, error) {
	storageCtx, storageCancel := context.WithTimeout(ctx, 25*time.Second)
	storageErr := sample(storageCtx)
	storageExpired := storageCtx.Err() != nil
	storageCancel()
	var plan executor.ReleaseCleanupPlan
	partial := func(reason string) {
		plan.State = "PARTIAL"
		if plan.Reason != "" {
			plan.Reason += "; "
		}
		plan.Reason += reason
		plan.NextRun = "next hourly host cleanup run"
	}
	budgetError := func(err error) bool {
		return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
	}
	if storageExpired || budgetError(storageErr) {
		plan.StorageState, plan.StorageReason = "PARTIAL", "storage step budget exhausted"
		storageErr = nil
	}
	if storageErr != nil {
		plan.StorageState, plan.StorageReason = "FAILED", storageErr.Error()
	}
	if plan.StorageState == "" {
		plan.StorageState = "COMPLETE"
	}
	var cleanupErr error
	switch {
	case ctx.Err() != nil:
		partial("worker budget exhausted before cleanup")
	case !ready():
		partial("cleanup postponed by host load")
	default:
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 45*time.Second)
		prior := plan
		plan, cleanupErr = cleanup(cleanupCtx)
		if cleanupCtx.Err() != nil || budgetError(cleanupErr) {
			partial("cleanup step budget exhausted")
			cleanupErr = nil
		}
		cleanupCancel()
		plan.StorageState, plan.StorageReason = prior.StorageState, prior.StorageReason
	}
	if plan.State == "" {
		plan.State = "COMPLETE"
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if len(raw) <= 1<<20 {
		_ = save(raw)
	}
	return raw, errors.Join(storageErr, cleanupErr)
}
