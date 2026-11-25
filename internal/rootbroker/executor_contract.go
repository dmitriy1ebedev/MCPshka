package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/pipelineartifact"
	"mcpshka/internal/recipecontract"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

var executorID = regexp.MustCompile("^[0-9a-f]{32}$")
var integrationApprovalID = regexp.MustCompile("^[0-9a-f]{64}$")

const executorHelper = "/usr/local/libexec/mcpshka-broker"

func validateExecutor(args []string) error {
	if len(args) == 2 && args[0] == "pipeline-receipt" && executorID.MatchString(args[1]) {
		return nil
	}
	if len(args) == 4 && args[0] == "pipeline-read" && executorID.MatchString(args[1]) {
		i, e := strconv.Atoi(args[2])
		off, e2 := strconv.ParseInt(args[3], 10, 64)
		if e == nil && e2 == nil && i >= 0 && i < 16 && strconv.Itoa(i) == args[2] && off >= 0 && off <= pipelineartifact.MaxArtifact && strconv.FormatInt(off, 10) == args[3] {
			return nil
		}
		return executor.ErrHold
	}
	if len(args) == 3 && args[0] == "release-receipt" && executorID.MatchString(args[1]) && executorID.MatchString(args[2]) {
		return nil
	}
	if len(args) == 4 && args[0] == "release-read" && executorID.MatchString(args[1]) && (args[2] == "controller" || args[2] == "broker") {
		offset, e := strconv.ParseInt(args[3], 10, 64)
		if e == nil && offset >= 0 && offset <= executor.MaxReleaseImage && strconv.FormatInt(offset, 10) == args[3] {
			return nil
		}
		return executor.Hold(failure.PolicyDenied, "RELEASE_OFFSET_INVALID", "rootbroker.validateExecutor", "release offset must be canonical and within image bounds", e)
	}
	if len(args) > 0 && (args[0] == "prepare-recipe" || args[0] == "abandon-recipe" || args[0] == "recipe-status") {
		return recipecontract.ValidateRecipeRequest(args, validateRecipePrepare)
	}
	if len(args) == 2 && args[0] == "candidate-publish" && executorID.MatchString(args[1]) {
		return nil
	}
	if len(args) == 3 && args[0] == "candidate-retire" && executorID.MatchString(args[1]) && (args[2] == "" || integrationApprovalID.MatchString(args[2])) {
		return nil
	}
	if len(args) == 3 && (integrationExecutorAction(args[0])) && executorID.MatchString(args[1]) && integrationApprovalID.MatchString(args[2]) {
		return nil
	}
	if len(args) != 3 || !executorID.MatchString(args[1]) || !executorID.MatchString(args[2]) {
		return errors.New("executor requires fixed action, permit and execution identities")
	}
	switch args[0] {
	case "launch", "status", "fence", "fence-status", "canary-fence", "canary-recover":
		return nil
	}
	return errors.New("unsupported executor operation")
}

// Outer proc masking conflicts with unprivileged bubblewrap proc mount.
// The trusted bootstrap has no CAP_SYS_ADMIN; user code gets private proc and no caps.
func executorUnitArgs(permitID, launchID string) []string {
	return heavyqueue.HeavyUnitArgs([]string{"--quiet", "--no-block", "--unit=mcpshka-executor-" + launchID + ".service", "--property=Type=exec", "--property=Restart=no", "--property=KillMode=control-group", "--property=RuntimeMaxSec=180s", "--property=TimeoutStopSec=5s", "--property=MemoryMax=512M", "--property=CPUQuota=25%", "--property=TasksMax=32", "--property=UMask=0077", "--property=NoNewPrivileges=yes", "--property=RestrictSUIDSGID=no", "--property=User=root", "--property=Group=root", "--property=PrivateNetwork=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes", "--property=ProtectSystem=strict", "--property=ProtectHome=yes", "--property=ProtectControlGroups=yes", "--property=ProtectKernelModules=yes", "--property=CapabilityBoundingSet=CAP_SETUID CAP_SETGID CAP_DAC_OVERRIDE CAP_KILL", "--property=AmbientCapabilities=CAP_SETUID CAP_SETGID", "--property=ReadWritePaths=" + executor.AuthorityRoot + " " + heavyqueue.Directory, "--property=BindPaths=" + executor.AuthorityRoot + "/workspaces/" + permitID + ":" + executor.WorkspaceMountPath, executorHelper, "executor-worker", permitID, launchID})
}
func runExecutor(ctx context.Context, args []string) (result []byte, retErr error) {
	budget := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			retErr = failure.Context(ctx, "rootbroker.runExecutor", budget, retErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if e := validateExecutor(args); e != nil {
		return nil, e
	}
	if args[0] == "pipeline-receipt" {
		if err := executor.PrunePipelineArtifacts(ctx); err != nil {
			return nil, err
		}
		out, err := executor.PipelineReceipt(ctx, args[1])
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	if args[0] == "pipeline-read" {
		i, _ := strconv.Atoi(args[2])
		off, _ := strconv.ParseInt(args[3], 10, 64)
		raw, eof, err := executor.PipelineRead(ctx, args[1], i, off)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"data": raw, "eof": eof})
	}
	if args[0] == "release-receipt" {
		r, e := executor.LocalReleaseReceipt(ctx, args[1], args[2])
		if e != nil {
			return nil, e
		}
		return json.Marshal(r)
	}
	if args[0] == "release-read" {
		offset, _ := strconv.ParseInt(args[3], 10, 64)
		raw, eof, e := executor.LocalReleaseRead(ctx, args[1], args[2], offset)
		if e != nil {
			return nil, e
		}
		return json.Marshal(map[string]any{"data": raw, "eof": eof})
	}
	if args[0] == "prepare-recipe" {
		return runRecipePrepare(ctx, args)
	}
	if args[0] == "abandon-recipe" {
		return runRecipeAbandon(ctx, args)
	}
	if args[0] == "recipe-status" {
		return runRecipeStatus(ctx, args)
	}
	if args[0] == "candidate-publish" {
		r, err := executor.PublishApprovedCandidate(ctx, args[1])
		if err != nil {
			return nil, err
		}
		return json.Marshal(r)
	}
	if args[0] == "candidate-retire" {
		r, err := executor.RetireCandidateImage(ctx, args[1], args[2])
		if err != nil {
			return nil, err
		}
		return json.Marshal(r)
	}
	c, m, e := executor.OpenPermit(args[1])
	if e != nil {
		return nil, e
	}
	// Permits are root-prepared: synthetic runtime or read-only committed tests.
	// Administrative project lease fencing is still unavailable.
	if m.Purpose != "canary-v1" && m.Purpose != "project-test-v1" && m.Purpose != "project-recipe-test-v1" && m.Purpose != executor.ModuleTestPurpose && m.Purpose != executor.DistributedShardPurpose && m.Purpose != executor.CIEvidencePurpose && m.Purpose != executor.LocalReleasePurpose {
		return nil, executor.Hold(failure.PolicyDenied, "EXECUTOR_PURPOSE_UNSUPPORTED", "rootbroker.runExecutor", "sealed permit purpose unsupported", nil)
	}
	if integrationExecutorAction(args[0]) {
		a, err := executor.IntegrationApprovalStatus(ctx, args[2])
		if err != nil || a.TestPin != m.Pin {
			return nil, executor.Hold(failure.FencedUnknown, "EXECUTOR_APPROVAL_MISMATCH", "rootbroker.runExecutor", "integration approval does not match sealed permit", err)
		}
		return runIntegrationAction(ctx, args, a)
	}
	if args[0] == "status" {
		if _, ok := m.Runs[args[2]]; !ok {
			return nil, executor.Hold(failure.PolicyDenied, "EXECUTOR_LAUNCH_ABSENT", "rootbroker.runExecutor.status", "launch absent from sealed manifest", nil)
		}
		s, e := c.Inspect(ctx, m.Pin)
		if e != nil {
			return nil, e
		}
		s.Source = m.Source
		s.Queue, _ = heavyqueue.Host().Lookup("executor:" + args[1])
		return json.Marshal(s)
	}
	if args[0] == "canary-fence" || args[0] == "canary-recover" {
		if m.Purpose != "canary-v1" && m.Purpose != "project-test-v1" && m.Purpose != "project-recipe-test-v1" && m.Purpose != executor.ModuleTestPurpose && m.Purpose != executor.DistributedShardPurpose && m.Purpose != executor.CIEvidencePurpose && m.Purpose != executor.LocalReleasePurpose {
			return nil, executor.Hold(failure.PolicyDenied, "EXECUTOR_FENCE_PURPOSE_UNSUPPORTED", "rootbroker.runExecutor.fence", "sealed permit purpose cannot be fenced here", nil)
		}
		if _, ok := m.Runs[args[2]]; !ok {
			return nil, executor.Hold(failure.PolicyDenied, "EXECUTOR_FENCE_LAUNCH_ABSENT", "rootbroker.runExecutor.fence", "fence launch absent from sealed manifest", nil)
		}
		e = c.WithPermitLock(ctx, m.Pin, func() error {
			if args[0] == "canary-recover" {
				return c.RecoverFence(ctx, m.Pin, executor.SystemdFence{})
			}
			return c.Fence(ctx, m.Pin, executor.SystemdFence{})
		})
		if e != nil {
			return nil, e
		}
		s, e := c.Inspect(ctx, m.Pin)
		if e != nil {
			return nil, e
		}
		s.Source = m.Source
		return json.Marshal(s)
	}
	if args[0] == "fence" || args[0] == "fence-status" {
		// The last argument is a root-issued one-use approval ID, not a PID,
		// unit name, holder secret or authorization boolean from the caller.
		if args[0] == "fence" {
			if e := c.AdministrativeFence(ctx, m.Pin, args[2]); e != nil {
				return nil, e
			}
		}
		a, e := c.AdministrativeFenceStatus(ctx, m.Pin, args[2])
		if e != nil {
			return nil, e
		}
		s, e := c.Inspect(ctx, m.Pin)
		if e != nil {
			return nil, e
		}
		s.Source = m.Source
		return json.Marshal(map[string]any{"state": a.State, "approval": a, "executor": s})
	}
	if _, ok := m.Runs[args[2]]; !ok {
		return nil, executor.Hold(failure.PolicyDenied, "EXECUTOR_LAUNCH_ABSENT", "rootbroker.runExecutor.launch", "launch absent from sealed manifest", nil)
	}
	if e = validateRootOwnedExecutable(executorHelper); e != nil {
		return nil, e
	}
	e = c.WithPermitLock(ctx, m.Pin, func() error {
		if e := c.Reserve(ctx, m.Pin, args[2]); e != nil {
			return e
		}
		unitArgs := executorUnitArgs(args[1], args[2])
		if m.Purpose == "project-test-v1" {
			unitArgs = projectTestUnitArgs(args[1], args[2])
		}
		if m.Purpose == executor.ModuleTestPurpose || m.Purpose == executor.DistributedShardPurpose || m.Purpose == executor.LocalReleasePurpose {
			unitArgs = moduleTestUnitArgs(args[1], args[2])
		}
		if m.Purpose == "project-recipe-test-v1" {
			var err error
			unitArgs, err = recipeTestUnitArgs(args[1], args[2], m.Source.Package, m.Runs[args[2]])
			if err != nil {
				return err
			}
		}
		cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", unitArgs...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		var out limitedBuffer
		out.limit = 4096
		cmd.Stdout = &out
		cmd.Stderr = &out
		if e := cmd.Run(); e != nil || out.truncated {
			return executor.Hold(failure.FencedUnknown, "EXECUTOR_SCHEDULE_UNVERIFIED", "rootbroker.runExecutor.launch", "executor scheduling unverified; inspect unit before retry", e)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	// Accepted means durable scheduling intent, not registered/running/success.
	return json.Marshal(map[string]string{"state": "LAUNCH_INTENT", "permit_id": args[1], "launch_id": args[2]})
}

func projectTestUnitArgs(permitID, launchID string) []string {
	args := executorUnitArgs(permitID, launchID)
	for i, a := range args {
		if a == "--property=RuntimeMaxSec=180s" {
			args[i] = "--property=RuntimeMaxSec=23400s"
		}
	}
	return args
}

// Whole-module tests compile every package: two CPUs, room for the linker and
// go vet, and one hour. The disk scratch lives under the existing writable
// authority root, so no new path is exposed to the unit.
func moduleTestUnitArgs(permitID, launchID string) []string {
	args := executorUnitArgs(permitID, launchID)
	for i, a := range args {
		switch a {
		case "--property=RuntimeMaxSec=180s":
			args[i] = "--property=RuntimeMaxSec=25200s"
		case "--property=MemoryMax=512M":
			args[i] = "--property=MemoryMax=1600M"
		case "--property=CPUQuota=25%":
			args[i] = "--property=CPUQuota=200%"
		case "--property=TasksMax=32":
			args[i] = "--property=TasksMax=256"
		}
	}
	// Insert the scratch bind before the helper and its fixed arguments.
	tail := len(args) - 4
	extra := []string{"--property=BindPaths=" + executor.ModuleScratchRoot + "/" + permitID + ":" + executor.ScratchMountPath}
	return append(args[:tail:tail], append(extra, args[tail:]...)...)
}
