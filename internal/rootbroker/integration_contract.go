package rootbroker

import (
	"context"
	"encoding/json"
	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"os/exec"
	"time"
)

func integrationExecutorAction(action string) bool {
	switch action {
	case "integration-status", "integration-recovery-status", "integration-recover", "integration-launch", "integration-worker-status", "integration-worker-fence", "integration-worker-recover", "integration-abort":
		return true
	}
	return false
}
func integrationUnitArgs(m executor.IntegrationLaunch) []string {
	return heavyqueue.HeavyUnitArgs([]string{"--quiet", "--no-block", "--unit=mcpshka-executor-" + m.LaunchID + ".service",
		"--property=Type=exec", "--property=Restart=no", "--property=KillMode=control-group",
		"--property=RuntimeMaxSec=780s", "--property=TimeoutStopSec=5s",
		"--property=MemoryMax=512M", "--property=CPUQuota=25%", "--property=TasksMax=32",
		"--property=UMask=0077", "--property=NoNewPrivileges=yes", "--property=RestrictSUIDSGID=yes",
		"--property=User=root", "--property=Group=root", "--property=PrivateNetwork=yes",
		"--property=PrivateTmp=yes", "--property=PrivateDevices=yes", "--property=ProtectSystem=strict",
		"--property=ProtectHome=read-only", "--property=ProtectControlGroups=yes", "--property=ProtectKernelModules=yes",
		"--property=CapabilityBoundingSet=CAP_SETUID CAP_SETGID CAP_DAC_OVERRIDE CAP_KILL",
		// Under NoNewPrivileges with seccomp-backed sandboxing systemd leaves
		// CAP_SETUID out of root's effective set; the import reads the source
		// repository as the service user, so keep it ambient like other units.
		"--property=AmbientCapabilities=CAP_SETUID CAP_SETGID",
		"--property=ReadWritePaths=" + executor.AuthorityRoot + " " + m.Claim.Repository.CommonDir,
		"--property=InaccessiblePaths=-" + DefaultSocketPath,
		executorHelper, "executor-integration-worker", m.Pin.PermitID, m.LaunchID})
}
func runIntegrationAction(ctx context.Context, args []string, a executor.IntegrationApproval) (result []byte, retErr error) {
	budget := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			retErr = failure.Context(ctx, "rootbroker.runIntegrationAction", budget, retErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch args[0] {
	case "integration-status":
		return json.Marshal(a)
	case "integration-recovery-status":
		claim, err := executor.IntegrationRecoveryStatus(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(claim)
	case "integration-recover":
		claim, err := executor.RecoverIntegration(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(claim)
	case "integration-worker-status":
		state, err := executor.IntegrationRuntimeStatus(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	case "integration-worker-fence":
		state, err := executor.FenceIntegrationWorker(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	case "integration-worker-recover":
		state, err := executor.RecoverIntegrationWorker(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	case "integration-abort":
		state, err := executor.AbortProtectedIntegration(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	case "integration-launch":
		// An existing attempt is observed, never sent to systemd again.
		if state, err := executor.IntegrationRuntimeStatus(ctx, a.ID); err == nil && state.Dispatched {
			return json.Marshal(state)
		}
		m, err := executor.PrepareIntegrationLaunch(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		if err := validateRootOwnedExecutable(executorHelper); err != nil {
			return nil, err
		}
		c, err := executor.OpenAuthority()
		if err != nil {
			return nil, err
		}
		err = c.DispatchIntegration(ctx, m, func() error {
			launchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(launchCtx, "/usr/bin/systemd-run", integrationUnitArgs(m)...)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
			var out limitedBuffer
			out.limit = 4096
			cmd.Stdout = &out
			cmd.Stderr = &out
			cmd.WaitDelay = 3 * time.Second
			if err := cmd.Run(); err != nil || out.truncated {
				if launchCtx.Err() != nil {
					return failure.Context(launchCtx, "rootbroker.runIntegrationAction.dispatch", 10*time.Second, err)
				}
				return executor.Hold(failure.FencedUnknown, "INTEGRATION_SCHEDULE_UNVERIFIED", "rootbroker.runIntegrationAction.dispatch", "integration scheduling unverified; inspect unit before retry", err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		state, err := executor.IntegrationRuntimeStatus(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	}
	return nil, executor.Hold(failure.PolicyDenied, "INTEGRATION_ACTION_UNSUPPORTED", "rootbroker.runIntegrationAction", "integration action unsupported", nil)
}
