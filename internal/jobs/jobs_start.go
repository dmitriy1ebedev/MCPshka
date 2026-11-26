package jobs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/policy"
)

func (m *Manager) Start(command string, args []string, workdir string) (Snapshot, error) {
	return m.StartWithOptions(command, args, workdir, StartOptions{})
}

func (m *Manager) StartWithOptions(command string, args []string, workdir string, opts StartOptions) (outSnapshot Snapshot, launchErr error) {
	mayHaveStarted := false
	defer func() {
		if launchErr != nil && !mayHaveStarted {
			launchErr = rejectBeforeLaunch(launchErr)
		}
	}()
	cwd, err := m.policy.CheckWorkdir(workdir)
	if err != nil {
		m.log(audit.Event{Type: "job_rejected", Command: command, Args: args, Workdir: workdir, Result: err.Error()})
		return Snapshot{}, err
	}
	m.mu.RLock()
	external := m.external
	m.mu.RUnlock()
	matchedExternal := external != nil && external.Match(command, args, cwd)
	decision, err := m.commandDecision(command, args, cwd, matchedExternal)
	approvalID := ""
	if err != nil && decision.Class == policy.ClassDestructive &&
		decision.Reason == "command class is not enabled" &&
		decision.Profile == policy.ProfileOperator &&
		opts.ApprovalToken != "" && m.approvals != nil {
		approved, approvalErr := m.approvals.ValidateWithPlan(opts.ApprovalToken, command, args, cwd, opts.PlanHash)
		if approvalErr == nil {
			approvalID = approved.ID
			decision.Allowed = true
			decision.Reason = "one-time destructive approval"
			err = nil
		} else {
			err = fmt.Errorf("destructive approval rejected: %w", approvalErr)
		}
	}
	if err != nil {
		err = withPolicyHint(err, command)
		m.log(audit.Event{
			Type: "job_rejected", Command: command, Args: args, Workdir: cwd, Result: err.Error(),
			Details: map[string]any{"command_class": decision.Class, "policy_reason": decision.Reason, "role_profile": decision.Profile},
		})
		return Snapshot{}, err
	}
	if err := checkTasksHeadroom(); err != nil {
		m.log(audit.Event{Type: "job_rejected", Command: command, Args: args, Workdir: cwd, Result: err.Error()})
		return Snapshot{}, err
	}
	outputLimit := opts.OutputLimitBytes
	if outputLimit <= 0 {
		outputLimit = m.defaultOutputCap
	}
	if outputLimit > MaxJobOutputLimit {
		outputLimit = MaxJobOutputLimit
	}
	// One flexible rule for every job (internal/budget): history, host load,
	// stall watchdog, extension while progressing, one ceiling.
	plan := m.Budget(command, args, cwd, opts.Timeout)
	timeout := min(plan.Wall, MaxJobTimeout)
	baseTimeout := opts.Timeout
	if baseTimeout <= 0 {
		baseTimeout = m.defaultTimeout
	}
	if opts.InteractiveOwner != "" {
		timeout = min(baseTimeout, DefaultJobTimeout)
	}
	memoryLimit := opts.MemoryLimitBytes
	if memoryLimit <= 0 {
		memoryLimit = m.defaultMemoryLimit
	}
	if memoryLimit > MaxMemoryLimit {
		memoryLimit = MaxMemoryLimit
	}
	cpuTimeLimit := opts.CPUTimeLimit
	cpuBase := baseTimeout
	if cpuTimeLimit <= 0 {
		// The default CPU budget belongs to the default wall time: an explicit
		// timeout_seconds=7200 kept 1800 CPU-s and could end a CPU-heavy
		// matrix the wall budget allowed (DIAG ACTIVE-06, MCP-122).
		cpuTimeLimit, cpuBase = m.defaultCPUTimeLimit, m.defaultTimeout
	}
	// A budget raised by history or load raises the CPU ceiling with it, or
	// the CPU limit would end the very run the wall budget allows.
	if timeout > cpuBase && cpuBase > 0 {
		cpuTimeLimit = time.Duration(float64(cpuTimeLimit) * float64(timeout) / float64(cpuBase))
	}
	if cpuTimeLimit > MaxCPUTimeLimit {
		cpuTimeLimit = MaxCPUTimeLimit
	}
	processLimit := opts.ProcessLimit
	if processLimit <= 0 {
		processLimit = m.defaultProcessLimit
	}
	if processLimit > MaxProcessLimit {
		processLimit = MaxProcessLimit
	}
	cpuQuotaPercent := opts.CPUQuotaPercent
	if cpuQuotaPercent <= 0 {
		cpuQuotaPercent = DefaultCPUQuotaPercent
	}
	if cpuQuotaPercent > MaxCPUQuotaPercent {
		cpuQuotaPercent = MaxCPUQuotaPercent
	}

	m.ReconcileUnknown(16)
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		err := errors.New("job manager is shutting down")
		m.log(audit.Event{Type: "job_rejected", Command: command, Args: args, Workdir: cwd, Result: err.Error()})
		return Snapshot{}, err
	}
	if m.cacheMaintenance && (matchedExternal || !gitIndependentOfGoCache(command, args)) {
		m.mu.Unlock()
		return Snapshot{}, goCacheRefusal()
	}
	active := m.starting
	if opts.InteractiveOwner != "" {
		sessions := m.starting
		for _, j := range m.jobs {
			if j.SessionOwner != "" && pendingWriter(j) {
				sessions++
			}
		}
		if sessions >= MaxInteractiveSessions {
			m.mu.Unlock()
			return Snapshot{}, errors.New("interactive session limit reached")
		}
	}
	queued := 0
	for _, j := range m.jobs {
		if j.State == Queued {
			queued++
		}
		if pendingWriter(j) && j.State != Queued {
			active++
		}
	}
	if queued >= 32 && heavyJobPriority(command, args, opts) {
		m.mu.Unlock()
		return Snapshot{}, errors.New("heavy task queue capacity reached: 32")
	}
	if active >= m.maxConcurrent && !heavyJobPriority(command, args, opts) {
		m.mu.Unlock()
		err := fmt.Errorf("concurrent job limit reached: %d", m.maxConcurrent)
		m.log(audit.Event{Type: "job_rejected", Command: command, Args: args, Workdir: cwd, Result: err.Error()})
		return Snapshot{}, err
	}
	if opts.HolderToken != "" {
		if opts.ReservedID != "" || m.holderGuard == nil {
			m.mu.Unlock()
			return Snapshot{}, errors.New("lease holder admission unavailable: HOLD")
		}
		if err := m.holderGuard(cwd, opts.HolderToken); err != nil {
			m.mu.Unlock()
			return Snapshot{}, holderRefusal(cwd, err)
		}
	} else if m.leaseGuard != nil {
		if err := m.leaseGuard(cwd, opts.ReservedID); err != nil {
			m.mu.Unlock()
			return Snapshot{}, err
		}
	}
	if opts.ReservedID != "" && !validReservedID(opts.ReservedID) {
		m.mu.Unlock()
		return Snapshot{}, errors.New("invalid managed job identity")
	}
	if opts.ReservedID != "" && m.database == nil {
		m.mu.Unlock()
		return Snapshot{}, errors.New("managed launch requires SQLite")
	}
	m.starting++
	m.seq++
	id := strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + strconv.FormatUint(m.seq, 36)
	if opts.ReservedID != "" {
		id = opts.ReservedID
	}
	m.mu.Unlock()

	releaseReservation := func() {
		m.mu.Lock()
		if m.starting > 0 {
			m.starting--
		}
		m.mu.Unlock()
	}

	if opts.InteractiveOwner != "" && (matchedExternal || heavyJobPriority(command, args, opts)) {
		releaseReservation()
		return Snapshot{}, errors.New("interactive sessions require a local lightweight command")
	}
	if matchedExternal {
		opts.OutputLimitBytes = outputLimit
		opts.Timeout = timeout
		opts.MemoryLimitBytes = memoryLimit
		opts.CPUTimeLimit = cpuTimeLimit
		opts.ProcessLimit = processLimit
		opts.CPUQuotaPercent = cpuQuotaPercent
		mayHaveStarted = true
		s, err := m.startExternal(external, id, command, args, cwd, opts)
		if err == nil && opts.HolderToken != "" {
			go m.keepHolderAlive(s.ID, cwd, opts.HolderToken)
		}
		return s, err
	}

	jobDir := filepath.Join(m.root, id)
	if err := os.Mkdir(jobDir, 0700); err != nil {
		releaseReservation()
		return Snapshot{}, err
	}
	stdoutPath := filepath.Join(jobDir, "stdout.log")
	stderrPath := filepath.Join(jobDir, "stderr.log")
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		releaseReservation()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		releaseReservation()
		_ = stdout.Close()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, err
	}

	quota := &outputQuota{limit: outputLimit}
	cmd := exec.Command(command, args...)
	scopeUnit := ""
	if cmd.Err == nil {
		launchCommand, launchArgs := cmd.Path, args
		if heavyJobPriority(command, args, opts) {
			launchCommand, launchArgs = lowPriorityCommand(cmd.Path, args)
		}
		if m.SystemdScopesEnabled() {
			scopeUnit, cmd = buildScopedCommand(id, launchCommand, launchArgs, memoryLimit, cpuQuotaPercent, processLimit)
		} else {
			cmd = exec.Command(launchCommand, launchArgs...)
		}
	}

	cmd, gateReader, gateWriter, err := permitCommand(cmd)
	if err != nil {
		releaseReservation()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, err
	}
	defer gateReader.Close()
	defer gateWriter.Close()
	var ticket *heavyqueue.Ticket
	handedOff := false
	if heavyJobPriority(command, args, opts) {
		queue := m.heavyQueue
		ready := queue.Ready
		queue.Ready = func() (bool, string) {
			m.mu.RLock()
			active := m.starting
			for _, job := range m.jobs {
				if pendingWriter(job) && job.State != Queued {
					active++
				}
			}
			limit := m.maxConcurrent
			m.mu.RUnlock()
			if active >= limit {
				return false, "concurrent job slots occupied"
			}
			if ready != nil {
				return ready()
			}
			return heavyqueue.HostReady()
		}
		ticket, err = queue.EnqueueScoped("job:"+id, cwd)
		if err != nil {
			releaseReservation()
			stdout.Close()
			stderr.Close()
			os.RemoveAll(jobDir)
			return Snapshot{}, err
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, ticket.File)
		defer func() {
			if !handedOff {
				ticket.Close()
			}
		}()
	}

	cmd.Dir = cwd
	jobEnv := opts.Env
	if opts.PrivateTemp {
		if _, supplied := opts.Env["TMPDIR"]; supplied {
			releaseReservation()
			_ = stdout.Close()
			_ = stderr.Close()
			_ = os.RemoveAll(jobDir)
			return Snapshot{}, errors.New("managed temporary directory cannot be overridden")
		}
		tmpDir := filepath.Join(jobDir, "tmp")
		if err := os.Mkdir(tmpDir, 0700); err != nil {
			releaseReservation()
			_ = stdout.Close()
			_ = stderr.Close()
			_ = os.RemoveAll(jobDir)
			return Snapshot{}, err
		}
		jobEnv = make(map[string]string, len(opts.Env)+1)
		for key, value := range opts.Env {
			jobEnv[key] = value
		}
		jobEnv["TMPDIR"] = tmpDir
	}
	env, err := policy.ExtendEnv(policy.MinimalEnv(m.home), jobEnv)
	if err != nil {
		releaseReservation()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, err
	}
	cmd.Env = env
	cmd.Stdout = &quotaWriter{dst: stdout, quota: quota}
	cmd.Stderr = &quotaWriter{dst: stderr, quota: quota}

	if approvalID != "" {
		if _, err := m.approvals.ConsumeWithPlan(opts.ApprovalToken, command, args, cwd, opts.PlanHash); err != nil {
			releaseReservation()
			_ = stdout.Close()
			_ = stderr.Close()
			_ = os.RemoveAll(jobDir)
			return Snapshot{}, fmt.Errorf("consume destructive approval: %w", err)
		}
	}

	var input, terminal *os.File
	if opts.InteractiveOwner != "" {
		input, terminal, err = prepareInteractive(cmd, opts.PTY)
		if err != nil {
			releaseReservation()
			stdout.Close()
			stderr.Close()
			os.RemoveAll(jobDir)
			return Snapshot{}, err
		}
		defer cmd.Stdin.(*os.File).Close()
		defer func() {
			if launchErr != nil {
				input.Close()
				if terminal != nil {
					terminal.Close()
				}
			}
		}()
	}
	j := &Job{
		SessionOwner: opts.InteractiveOwner, input: input, terminal: terminal, lastInteraction: time.Now(),
		ID: id, Command: command, CommandClass: decision.Class,
		Args: append([]string(nil), args...), Workdir: cwd,
		State: Running, StartedAt: time.Now().UTC(), cmd: cmd,
		stdoutPath: stdoutPath, stderrPath: stderrPath,
		quota: quota, done: make(chan struct{}),
		idle: plan.Idle, budgetNote: plan.String(),
		TimeoutSeconds:      int64(timeout / time.Second),
		OutputLimitBytes:    outputLimit,
		MemoryLimitBytes:    memoryLimit,
		CPUTimeLimitSeconds: int64(cpuTimeLimit / time.Second),
		ProcessLimit:        processLimit,
		CPUQuotaPercent:     cpuQuotaPercent,
		ScopeUnit:           scopeUnit,
	}
	if ticket != nil {
		j.State = Queued
		j.queueDone = make(chan struct{})
		status, _ := ticket.Status()
		j.Queue = &status
	}
	m.mu.Lock()
	if m.closing {
		if m.starting > 0 {
			m.starting--
		}
		m.mu.Unlock()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, errors.New("job manager is shutting down")
	}
	mayHaveStarted = true
	start := cmd.Start
	if dir := m.heavyCgroup; dir != "" && heavyJobPriority(command, args, opts) {
		start = func() error { return startInCgroup(cmd, dir) }
	}
	if err := start(); err != nil {
		if m.starting > 0 {
			m.starting--
		}
		m.mu.Unlock()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, rejectBeforeLaunch(err) // fork/exec failed: no process exists
	}
	j.PID = cmd.Process.Pid
	j.BootID, j.StartTicks, err = processIdentity(j.PID)
	if m.starting > 0 {
		m.starting--
	}
	m.jobs[id] = j
	initial := snapshot(j)
	m.mu.Unlock()

	if err == nil {
		err = m.persist(initial)
	}
	if err == nil && opts.ReservedID != "" {
		err = m.persistLaunch(initial)
	}
	if err == nil && ticket == nil {
		_, err = gateWriter.Write([]byte{'1'})
	}
	if err != nil {
		if j.ScopeUnit != "" {
			terminateScope(j.ScopeUnit, syscall.SIGKILL)
		}
		_ = syscall.Kill(-j.PID, syscall.SIGKILL)
		_ = cmd.Wait()
		if j.queueDone != nil {
			close(j.queueDone)
		}
		_ = stdout.Close()
		_ = stderr.Close()
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
		_ = os.RemoveAll(jobDir)
		return Snapshot{}, fmt.Errorf("persist job metadata: %w", err)
	}
	m.log(audit.Event{
		Type: "job_started", JobID: id, Command: command, Args: args,
		Workdir: cwd, Result: string(initial.State),
		Details: map[string]any{
			"command_class":          decision.Class,
			"timeout_seconds":        timeout / time.Second,
			"budget":                 plan.String(),
			"output_limit_bytes":     outputLimit,
			"memory_limit_bytes":     memoryLimit,
			"cpu_time_limit_seconds": cpuTimeLimit / time.Second,
			"process_limit":          processLimit,
			"cpu_quota_percent":      cpuQuotaPercent,
			"scope_unit":             scopeUnit,
			"approval_id":            approvalID,
		},
	})

	if opts.HolderToken != "" {
		go m.keepHolderAlive(id, cwd, opts.HolderToken)
	}
	if ticket == nil {
		go m.watchTimeout(j, timeout)
		go m.watchResources(j)
	} else {
		dup, e := syscall.Dup(int(gateWriter.Fd()))
		if e != nil {
			close(j.queueDone)
			syscall.Kill(-j.PID, syscall.SIGKILL)
		} else {
			syscall.CloseOnExec(dup)
			handedOff = true
			go m.admitHeavyJob(j, ticket, os.NewFile(uintptr(dup), "queued-permit"), timeout)
		}
	}
	if terminal != nil {
		j.terminalDone = make(chan struct{})
		go func() { defer close(j.terminalDone); _, _ = io.Copy(&quotaWriter{dst: stdout, quota: quota}, terminal) }()
	}
	if opts.InteractiveOwner != "" {
		ttl := opts.InteractiveIdle
		if ttl <= 0 || ttl > InteractiveIdleTTL {
			ttl = InteractiveIdleTTL
		}
		go m.watchInteractive(j, ttl)
	}
	go m.wait(j, stdout, stderr)
	return initial, nil
}

// projectRunners are interpreters and build tools exec_start refuses as
// privileged while the sealed project_test/project_build profile runs them
// (27.10: "node ... not allowed" read as a dead end).
var projectRunners = map[string]bool{"node": true, "npm": true, "npx": true, "pnpm": true, "yarn": true, "python": true, "python3": true, "pytest": true, "go": true, "make": true, "cargo": true, "bash": true, "sh": true}

func withPolicyHint(err error, command string) error {
	if !projectRunners[filepath.Base(command)] {
		return err
	}
	return fmt.Errorf("%w; hint: a project's tests and builds run in the sealed trusted profile — use project_test / project_build (or project_run for a registered command), not exec_start", err)
}

// holderRefusal marks a lease-holder admission refusal as made before any
// job started, so the caller's retry key survives (BUG-20261103-5: a stale
// heartbeat or a token for another worktree burnt project_test and
// forge_ci keys on 0.3.1).
func holderRefusal(cwd string, err error) error {
	if failure.IsNoEffect(err) {
		return err
	}
	if e := failure.Details(err); e != nil {
		e.NoEffect = true
		return err
	}
	e := failure.Rejected("LEASE_HOLDER_REFUSED", "jobs.holderGuard", "no job started", err)
	e.Class = failure.EvidenceMissing
	e.NextSafeAction = failure.Action{Tool: "project_lease_status", Arguments: map[string]any{"path": cwd}}
	return e
}
