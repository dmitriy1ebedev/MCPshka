package jobs

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"mcpshka/internal/approval"
	"mcpshka/internal/audit"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

const (
	DefaultReadLimit       = 16 * 1024
	MaxReadLimit           = 64 * 1024
	DefaultJobOutputLimit  = int64(32 * 1024 * 1024)
	MaxJobOutputLimit      = int64(64 * 1024 * 1024)
	DefaultJobTimeout      = 30 * time.Minute
	MaxJobTimeout          = 6 * time.Hour
	DefaultMaxConcurrent   = 4
	DefaultMemoryLimit     = int64(1024 * 1024 * 1024)
	MaxMemoryLimit         = int64(2 * 1024 * 1024 * 1024)
	DefaultCPUTimeLimit    = 30 * time.Minute
	MaxCPUTimeLimit        = 6 * time.Hour
	DefaultProcessLimit    = 64
	MaxProcessLimit        = 256
	DefaultCPUQuotaPercent = 200
	MaxCPUQuotaPercent     = 400
	ResourcePollInterval   = 500 * time.Millisecond
	defaultClockTicks      = uint64(100)
	metadataName           = "metadata.json"
)

var ErrNotFound = errors.New("job not found")

type State string

const (
	Queued     State = "QUEUED"
	Running    State = "running"
	Done       State = "done"
	Idle       State = "idle"
	NoEffect   State = "no_effect"
	Partial    State = "partial"
	Failed     State = "failed"
	Cancelling State = "cancelling"
	Cancelled  State = "cancelled"
	TimedOut   State = "timed_out"
	Unknown    State = "unknown"
)

type StartOptions struct {
	InteractiveOwner   string // Internal authenticated principal, never supplied by exec_start.
	InteractiveIdle    time.Duration
	PTY                bool
	LowPriority        bool   // Internal: project test/build wrappers also run at low priority.
	DisableLowPriority bool   // Internal escape hatch; never exposed by MCP inputs.
	ReservedID         string // Internal managed workflow identity; never supplied by exec_start.
	PrivateTemp        bool   // Internal: temporary files live within the durable job directory.
	RequireExternal    bool   // Internal: a sealed workflow must never fall back to host execution.
	OutputLimitBytes   int64
	Timeout            time.Duration
	Env                map[string]string
	MemoryLimitBytes   int64
	CPUTimeLimit       time.Duration
	ProcessLimit       int
	CPUQuotaPercent    int
	ApprovalToken      string
	PlanHash           string
	// HolderToken lets the current interactive lease holder run jobs in its
	// own project; it is checked at admission and never persisted.
	HolderToken string
}

type Job struct {
	cancelReadOnly      context.CancelFunc
	SessionOwner        string `json:"session_owner,omitempty"`
	input               *os.File
	inputMu             sync.Mutex
	lastInteraction     time.Time
	terminal            *os.File
	terminalDone        chan struct{}
	Queue               *heavyqueue.Status `json:"queue,omitempty"`
	externalStarting    bool
	External            *ExternalBinding    `json:"external,omitempty"`
	ID                  string              "json:\"id\""
	Command             string              "json:\"command\""
	CommandClass        policy.CommandClass "json:\"command_class\""
	Args                []string            "json:\"args\""
	Workdir             string              "json:\"workdir\""
	State               State               "json:\"state\""
	PID                 int                 "json:\"pid\""
	BootID              string              `json:"boot_id,omitempty"`
	StartTicks          uint64              `json:"start_ticks,omitempty"`
	ResourcesReleased   bool                `json:"resources_released,omitempty"`
	ReconciledAt        time.Time           `json:"reconciled_at,omitempty"`
	ExitCode            *int                "json:\"exit_code,omitempty\""
	Error               string              "json:\"error,omitempty\""
	StartedAt           time.Time           "json:\"started_at\""
	FinishedAt          time.Time           "json:\"finished_at,omitempty\""
	TimeoutSeconds      int64               "json:\"timeout_seconds\""
	OutputLimitBytes    int64               "json:\"output_limit_bytes\""
	OutputStoredBytes   int64               "json:\"output_stored_bytes\""
	OutputDroppedBytes  int64               "json:\"output_dropped_bytes\""
	LastOutputAt        time.Time           "json:\"last_output_at,omitempty\""
	QuietSeconds        int64               "json:\"quiet_seconds\""
	MemoryLimitBytes    int64               "json:\"memory_limit_bytes\""
	CPUTimeLimitSeconds int64               "json:\"cpu_time_limit_seconds\""
	ProcessLimit        int                 "json:\"process_limit\""
	CPUQuotaPercent     int                 "json:\"cpu_quota_percent\""
	ScopeUnit           string              "json:\"scope_unit,omitempty\""
	ResourceViolation   string              "json:\"resource_violation,omitempty\""
	Paused              string              `json:"paused,omitempty"`
	PausedSeconds       int64               `json:"paused_seconds,omitempty"`

	queueDone  chan struct{}
	cmd        *exec.Cmd
	stdoutPath string
	stderrPath string
	quota      *outputQuota
	timedOut   bool
	done       chan struct{}

	// Flexible budget (internal/budget): stall window and progress marks.
	idle       time.Duration
	budgetNote string
	lastActive time.Time // last observed CPU, disk write or output (zero: none yet)
	observed   bool
	lastTicks  uint64
	lastWrite  uint64

	// Memory pause (jobs_pause.go).
	pausedAt, pauseChecked, pauseCooldown time.Time
}

type Snapshot struct {
	SessionOwner        string              `json:"session_owner,omitempty"`
	Queue               *heavyqueue.Status  `json:"queue,omitempty"`
	External            *ExternalBinding    `json:"external,omitempty"`
	ID                  string              "json:\"id\""
	Command             string              "json:\"command\""
	CommandClass        policy.CommandClass "json:\"command_class\""
	Args                []string            "json:\"args\""
	Workdir             string              "json:\"workdir\""
	State               State               "json:\"state\""
	PID                 int                 "json:\"pid\""
	BootID              string              `json:"boot_id,omitempty"`
	StartTicks          uint64              `json:"start_ticks,omitempty"`
	ResourcesReleased   bool                `json:"resources_released,omitempty"`
	ReconciledAt        time.Time           `json:"reconciled_at,omitempty"`
	ExitCode            *int                "json:\"exit_code,omitempty\""
	Error               string              "json:\"error,omitempty\""
	StartedAt           time.Time           "json:\"started_at\""
	FinishedAt          time.Time           "json:\"finished_at,omitempty\""
	TimeoutSeconds      int64               "json:\"timeout_seconds\""
	OutputLimitBytes    int64               "json:\"output_limit_bytes\""
	OutputStoredBytes   int64               "json:\"output_stored_bytes\""
	OutputDroppedBytes  int64               "json:\"output_dropped_bytes\""
	LastOutputAt        time.Time           "json:\"last_output_at,omitempty\""
	QuietSeconds        int64               "json:\"quiet_seconds\""
	MemoryLimitBytes    int64               "json:\"memory_limit_bytes\""
	CPUTimeLimitSeconds int64               "json:\"cpu_time_limit_seconds\""
	ProcessLimit        int                 "json:\"process_limit\""
	CPUQuotaPercent     int                 "json:\"cpu_quota_percent\""
	ScopeUnit           string              "json:\"scope_unit,omitempty\""
	ResourceViolation   string              "json:\"resource_violation,omitempty\""
	Paused              string              `json:"paused,omitempty"`
	PausedSeconds       int64               `json:"paused_seconds,omitempty"`
	Process             *ProcessObservation "json:\"process,omitempty\""
}

type Output struct {
	JobID      string "json:\"job_id\""
	Stream     string "json:\"stream\""
	Text       string "json:\"text\""
	Cursor     int64  "json:\"cursor\""
	NextCursor int64  "json:\"next_cursor\""
	TotalBytes int64  "json:\"total_bytes\""
	Truncated  bool   "json:\"truncated\""
	State      State  "json:\"state\""
}

type Manager struct {
	llmStarting         bool
	heavyQueue          heavyqueue.Queue
	cacheMaintenance    bool
	external            ExternalRuntime
	mu                  sync.RWMutex
	leaseGuard          func(string, string) error
	holderGuard         func(string, string) error
	holderKeepalive     func(string, string) error
	reapReferences      func() (map[string]bool, error)
	jobs                map[string]*Job
	ephemeral           map[string]*ephemeralReceipt
	root                string
	policy              *policy.Policy
	audit               *audit.Logger
	home                string
	seq                 uint64
	starting            int
	reconcileCursor     string
	maxConcurrent       int
	defaultOutputCap    int64
	defaultTimeout      time.Duration
	loadAvg             func() float64 // host load for the flexible budget; tests pin it
	defaultMemoryLimit  int64
	defaultCPUTimeLimit time.Duration
	defaultProcessLimit int
	clockTicksPerSecond uint64
	scopeEnabled        bool
	heavyCgroup         string // delegated heavy/ cgroup, P5.3
	approvals           *approval.Store
	closing             bool
	database            *statestore.DB
}

func New(root string, p *policy.Policy) (*Manager, error) {
	return NewWithAudit(root, p, nil)
}

func NewWithAudit(root string, p *policy.Policy, logger *audit.Logger) (*Manager, error) {
	if p == nil {
		p = policy.Default()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	home := filepath.Join(root, ".home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	db, err := statestore.Active(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	m := &Manager{
		database: db,
		// Library managers stay within their own state root (including CI and
		// sandbox fixtures). The daemon explicitly enables the shared host queue.
		heavyQueue:          heavyqueue.Queue{Dir: filepath.Join(root, ".heavy-queue"), Poll: 5 * time.Second, Ready: func() (bool, string) { return true, "" }},
		jobs:                make(map[string]*Job),
		root:                root,
		policy:              p,
		audit:               logger,
		home:                home,
		maxConcurrent:       DefaultMaxConcurrent,
		defaultOutputCap:    DefaultJobOutputLimit,
		defaultTimeout:      DefaultJobTimeout,
		loadAvg:             loadAverage,
		defaultMemoryLimit:  DefaultMemoryLimit,
		defaultCPUTimeLimit: DefaultCPUTimeLimit,
		defaultProcessLimit: DefaultProcessLimit,
		clockTicksPerSecond: defaultClockTicks,
	}
	// Every Go job shares this HOME; with telemetry on, go leaves a detached
	// child writing there after the job ends (BUG-20261024-86): its writes
	// outlive the job's accounting and raced the test HOME cleanup (24.11).
	if err := disableGoTelemetry(home); err != nil {
		return nil, err
	}
	if err := m.loadExisting(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Get(id string) (Snapshot, error) {
	if f := foreignFor(id); f != nil {
		return f.Get(id)
	}
	m.mu.Lock()
	if j := m.jobs[id]; j != nil && j.External != nil {
		s, e := m.observeExternal(j, false)
		m.mu.Unlock()
		return s, e
	}
	m.mu.Unlock()
	m.mu.RLock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.RUnlock()
		return Snapshot{}, ErrNotFound
	}
	s := snapshot(j)
	pid := j.PID
	scopeUnit := j.ScopeUnit
	state := j.State
	m.mu.RUnlock()

	if pid > 0 && (state == Running || state == Cancelling) {
		var obs ProcessObservation
		if scopeUnit != "" {
			obs = observeScope(scopeUnit)
			if !obs.Alive {
				obs = observeProcessGroup(pid)
			}
		} else {
			obs = observeProcessGroup(pid)
		}
		s.Process = &obs
	}
	return s, nil
}

func (m *Manager) Read(id, stream string, cursor int64, limit int) (Output, error) {
	if f := foreignFor(id); f != nil {
		return f.Read(id, stream, cursor, limit)
	}
	m.mu.Lock()
	if j := m.jobs[id]; j != nil && j.External != nil {
		if _, err := m.observeExternal(j, false); err != nil {
			m.mu.Unlock()
			return Output{}, err
		}
	}
	m.mu.Unlock()
	m.mu.RLock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.RUnlock()
		return Output{}, ErrNotFound
	}
	state := j.State
	ephemeralView := j.Command == "mcpshka-knowledge-view"
	path := j.stdoutPath
	if stream == "stderr" {
		path = j.stderrPath
	} else if stream != "" && stream != "stdout" {
		m.mu.RUnlock()
		return Output{}, errors.New("stream must be stdout or stderr")
	}
	m.mu.RUnlock()
	if ephemeralView {
		return m.readEphemeral(id, stream, cursor, limit, state)
	}

	if limit <= 0 {
		limit = DefaultReadLimit
	}
	if limit > MaxReadLimit {
		limit = MaxReadLimit
	}

	f, err := os.Open(path)
	if err != nil {
		return Output{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Output{}, err
	}
	total := info.Size()
	// A negative cursor reads the last -cursor bytes, like Desktop
	// Commander's read_process_output offset -N: the tail in one call.
	if cursor < 0 {
		limit = int(min(int64(limit), -cursor))
		cursor = max(0, total+cursor)
	}
	if cursor > total {
		cursor = total
	}
	if _, err := f.Seek(cursor, io.SeekStart); err != nil {
		return Output{}, err
	}
	buf := make([]byte, limit)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return Output{}, err
	}
	next := cursor + int64(n)
	return Output{
		JobID: id, Stream: streamOrDefault(stream), Text: string(buf[:n]),
		Cursor: cursor, NextCursor: next, TotalBytes: total,
		Truncated: next < total, State: state,
	}, nil
}

func (m *Manager) Cancel(id string) (Snapshot, error) {
	if f := foreignFor(id); f != nil {
		if c, ok := f.(ForeignCanceller); ok {
			return c.Cancel(id)
		}
		return Snapshot{}, errors.New("this off-box job source cannot cancel; the job ends by its own timeout")
	}
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Snapshot{}, ErrNotFound
	}
	if j.External != nil {
		s, e := m.observeExternal(j, true)
		m.mu.Unlock()
		return s, e
	}
	if j.cancelReadOnly != nil {
		j.cancelReadOnly()
		j.State = Cancelling
		s := snapshot(j)
		m.mu.Unlock()
		return s, nil
	}
	if j.State == Unknown {
		m.mu.Unlock()
		return Snapshot{}, errors.New("job ownership is unknown: reconcile before cancellation")
	}
	if j.State != Running && j.State != Queued {
		s := snapshot(j)
		m.mu.Unlock()
		return s, nil
	}
	j.State = Cancelling
	pid := j.PID
	scopeUnit := j.ScopeUnit
	done := j.done
	s := snapshot(j)
	m.mu.Unlock()
	_ = m.persist(s)

	m.log(audit.Event{
		Type: "job_cancel_requested", JobID: s.ID, Command: s.Command,
		Args: s.Args, Workdir: s.Workdir, Result: string(Cancelling),
	})
	m.terminateJob(pid, scopeUnit, done)
	return m.Get(id)
}

func (m *Manager) terminateJob(pid int, scopeUnit string, _ <-chan struct{}) {
	if scopeUnit != "" {
		terminateScope(scopeUnit, syscall.SIGTERM)
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(-pid, syscall.SIGCONT) // a memory-paused group must see SIGTERM
	go func() {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		<-timer.C
		// The process-group leader may exit before its descendants. j.done tracks
		// cmd.Wait() for that leader, so it is not evidence that the whole group
		// is gone. Always perform an idempotent hard cleanup after the grace
		// period; ESRCH is harmless when the group already exited.
		if scopeUnit != "" {
			terminateScope(scopeUnit, syscall.SIGKILL)
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
}

func snapshot(j *Job) Snapshot {
	stored := j.OutputStoredBytes
	dropped := j.OutputDroppedBytes
	lastOutputAt := j.LastOutputAt
	if j.quota != nil {
		stored, dropped = j.quota.stats()
		lastOutputAt = j.quota.lastActivity()
	}
	quietFrom := j.StartedAt
	if !lastOutputAt.IsZero() {
		quietFrom = lastOutputAt
	}
	quietUntil := time.Now().UTC()
	if !j.FinishedAt.IsZero() {
		quietUntil = j.FinishedAt
	}
	quietSeconds := int64(0)
	if !quietFrom.IsZero() && quietUntil.After(quietFrom) {
		quietSeconds = int64(quietUntil.Sub(quietFrom) / time.Second)
	}
	return Snapshot{
		SessionOwner: j.SessionOwner,
		Queue:        cloneQueue(j.Queue), External: cloneExternal(j.External), ID: j.ID, Command: j.Command, CommandClass: j.CommandClass,
		Args:    append([]string(nil), j.Args...),
		Workdir: j.Workdir, State: j.State, PID: j.PID, ExitCode: j.ExitCode,
		BootID: j.BootID, StartTicks: j.StartTicks, ResourcesReleased: j.ResourcesReleased, ReconciledAt: j.ReconciledAt,
		Error: j.Error, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		TimeoutSeconds: j.TimeoutSeconds, OutputLimitBytes: j.OutputLimitBytes,
		OutputStoredBytes: stored, OutputDroppedBytes: dropped,
		LastOutputAt: lastOutputAt, QuietSeconds: quietSeconds,
		MemoryLimitBytes: j.MemoryLimitBytes, CPUTimeLimitSeconds: j.CPUTimeLimitSeconds,
		ProcessLimit: j.ProcessLimit, CPUQuotaPercent: j.CPUQuotaPercent,
		ScopeUnit: j.ScopeUnit, ResourceViolation: j.ResourceViolation,
		Paused: j.Paused, PausedSeconds: j.PausedSeconds,
	}
}

func streamOrDefault(s string) string {
	if s == "" {
		return "stdout"
	}
	return s
}

func isTerminal(s State) bool {
	return s == Done || s == Idle || s == NoEffect || s == Partial || s == Failed || s == Cancelled || s == TimedOut
}

func (m *Manager) Policy() *policy.Policy {
	return m.policy
}

func (m *Manager) AuditLogger() *audit.Logger {
	return m.audit
}

func (m *Manager) StateDir() string {
	return m.root
}

func (m *Manager) SetApprovalStore(store *approval.Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvals = store
}

func (m *Manager) ApprovalStore() *approval.Store {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.approvals
}

func (m *Manager) Database() *statestore.DB { return m.database }
