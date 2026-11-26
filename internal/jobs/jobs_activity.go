package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/policy"
)

// ProjectActivity exposes bounded, sanitized reconciliation evidence. A live
// job is evidence against recovery; an empty list never proves owner death.
type ProjectActivity struct {
	Count        int           `json:"count"`
	UnknownCount int           `json:"unknown_count"`
	Truncated    bool          `json:"truncated"`
	Jobs         []JobActivity `json:"jobs"`
}

type JobActivity struct {
	ID            string `json:"id"`
	State         State  `json:"state"`
	PID           int    `json:"pid"`
	CurrentWaiter bool   `json:"current_waiter"`
	Workdir       string `json:"-"` // mission occupancy only, not published
}

func (m *Manager) ProjectActivity(root string, limit int) ProjectActivity {
	m.ReconcileUnknown(16)
	return m.ProjectActivityReadOnly(root, limit)
}

// ProjectActivityReadOnly observes registry state without process probes or reconciliation.
func (m *Manager) ProjectActivityReadOnly(root string, limit int) ProjectActivity {
	root = filepath.Clean(root)
	if limit < 1 || limit > 64 {
		limit = 16
	}
	out := ProjectActivity{Jobs: []JobActivity{}}
	m.mu.RLock()
	for _, j := range m.jobs {
		work := filepath.Clean(j.Workdir)
		if work != root && !strings.HasPrefix(work, strings.TrimRight(root, string(os.PathSeparator))+string(os.PathSeparator)) {
			continue
		}
		state := j.State
		if (j.External != nil && !j.ResourcesReleased && isTerminal(state)) || (state == Failed && j.Error == "mcpshka restarted while job was active") {
			state = Unknown
		}
		if state != Running && state != Queued && state != Cancelling && state != Unknown {
			continue
		}
		out.Count++
		if state == Unknown {
			out.UnknownCount++
		}
		out.Jobs = append(out.Jobs, JobActivity{ID: j.ID, State: state, PID: j.PID, CurrentWaiter: j.cmd != nil, Workdir: work})
	}
	m.mu.RUnlock()
	sort.Slice(out.Jobs, func(i, j int) bool { return out.Jobs[i].ID < out.Jobs[j].ID })
	if len(out.Jobs) > limit {
		out.Jobs = out.Jobs[:limit]
		out.Truncated = true
	}
	return out
}

// StoreReceipt persists a bounded control-plane response without consuming a
// process slot. A receipt completing does not mean the detached operation did.
func (m *Manager) StoreReceipt(command string, args []string, workdir, output string) (Snapshot, error) {
	cwd, err := m.policy.CheckWorkdir(workdir)
	if err != nil {
		return Snapshot{}, err
	}
	if len(output) > MaxReadLimit {
		return Snapshot{}, errors.New("receipt output exceeds cap")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return Snapshot{}, errors.New("job manager is shutting down")
	}
	var id, dir string
	for attempt := 0; ; attempt++ {
		if attempt >= 1024 {
			return Snapshot{}, errors.New("receipt ID collision budget exhausted")
		}
		m.seq++
		id = strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + strconv.FormatUint(m.seq, 36)
		dir = filepath.Join(m.root, id)
		err := os.Mkdir(dir, 0700)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		break
	}

	stdout, stderr := filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")
	if err := os.WriteFile(stdout, []byte(output), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return Snapshot{}, err
	}
	if err := os.WriteFile(stderr, nil, 0600); err != nil {
		_ = os.RemoveAll(dir)
		return Snapshot{}, err
	}
	now := time.Now().UTC()
	j := &Job{ID: id, Command: command, CommandClass: policy.ClassReadOnly, Args: append([]string(nil), args...),
		Workdir: cwd, State: Done, StartedAt: now, FinishedAt: now, OutputLimitBytes: MaxReadLimit,
		OutputStoredBytes: int64(len(output)), stdoutPath: stdout, stderrPath: stderr}
	s := snapshot(j)
	if err := m.persist(s); err != nil {
		_ = os.RemoveAll(dir)
		return Snapshot{}, err
	}
	m.jobs[id] = j
	return s, nil
}

func (m *Manager) Reconcile(id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	if j.External != nil {
		return m.observeExternal(j, false)
	}
	if j.State != Unknown {
		return Snapshot{}, errors.New("only unknown jobs can be reconciled")
	}
	if j.ResourcesReleased {
		return snapshot(j), nil
	}
	gone, err := unknownGroupGone(j)
	if err != nil || !gone {
		return Snapshot{}, err
	}
	before := j.ReconciledAt
	j.ResourcesReleased = true
	j.ReconciledAt = time.Now().UTC()
	s := snapshot(j)
	if err := m.persist(s); err != nil {
		j.ResourcesReleased = false
		j.ReconciledAt = before
		return Snapshot{}, err
	}
	return s, nil
}

// RegistrySummary has no commands, arguments, environment or holder secrets.
func (m *Manager) RegistrySummary(limit int) ProjectActivity {
	return m.ProjectActivity(string(os.PathSeparator), limit)
}

func (m *Manager) ReconcileUnknown(limit int) {
	if limit < 1 || limit > 64 {
		limit = 16
	}
	m.mu.Lock()
	// Rotate a bounded page through all UNKNOWN entries. A persistent HOLD
	// must not starve later entries during periodic reconciliation.
	ids := make([]string, 0)
	for id, j := range m.jobs {
		if (j.State == Unknown && !j.ResourcesReleased) || (j.External != nil && !isTerminal(j.State)) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	start := sort.SearchStrings(ids, m.reconcileCursor)
	if start < len(ids) && ids[start] == m.reconcileCursor {
		start++
	}
	if start >= len(ids) {
		start = 0
	}
	selected := make([]string, 0, limit)
	for i := 0; i < len(ids) && i < limit; i++ {
		selected = append(selected, ids[(start+i)%len(ids)])
	}
	if len(selected) > 0 {
		m.reconcileCursor = selected[len(selected)-1]
	}
	m.mu.Unlock()
	for _, id := range selected {
		_, _ = m.Reconcile(id)
	}
}
