package jobs

import (
	"errors"
	"mcpshka/internal/audit"
	"mcpshka/internal/budget"
	"mcpshka/internal/failure"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validReservedID(id string) bool {
	if len(id) != 41 || !strings.HasPrefix(id, "workflow-") {
		return false
	}
	for _, c := range id[9:] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (m *Manager) SetLeaseGuard(guard func(string, string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaseGuard = guard
}

// SetHolderGuard admits jobs presented with the current interactive lease
// token. Without it a held lease blocks every tokenless and token job alike.
func (m *Manager) SetHolderGuard(guard func(string, string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holderGuard = guard
}

// SetHolderKeepalive renews the interactive lease of a job started with a
// holder token for as long as the job runs: a long sealed test no longer
// leaves the lease stale (LEASE_HEARTBEAT_STALE) behind the waiting AI.
func (m *Manager) SetHolderKeepalive(keepalive func(path, token string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holderKeepalive = keepalive
}

// HolderKeepaliveInterval is how often a running job renews its holder lease.
var HolderKeepaliveInterval = 2 * time.Minute

func (m *Manager) keepHolderAlive(id, path, token string) {
	m.mu.RLock()
	keepalive := m.holderKeepalive
	m.mu.RUnlock()
	if keepalive == nil || token == "" {
		return
	}
	t := time.NewTicker(HolderKeepaliveInterval)
	defer t.Stop()
	// Never past the job ceiling: an UNKNOWN job must not hold a lease forever.
	deadline := time.Now().Add(budget.Ceiling)
	for range t.C {
		m.mu.RLock()
		j, ok := m.jobs[id]
		live := ok && !isTerminal(j.State) && j.State != Unknown
		m.mu.RUnlock()
		if !live || time.Now().After(deadline) {
			return
		}
		if err := keepalive(path, token); err != nil {
			m.log(audit.Event{Type: "job_lease_keepalive", JobID: id, Workdir: path, Result: "stopped: " + err.Error()})
			return
		}
	}
}

// SetReapReferences supplies job IDs whose receipts other authorities (managed
// leases) still depend on. Without it the reaper keeps every guarded job.
func (m *Manager) SetReapReferences(references func() (map[string]bool, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapReferences = references
}

// reapReferencedJobs loads lease references once per pass, and only when a
// candidate's workdir has vanished. A failed read yields nil: keep everything.
func (m *Manager) reapReferencedJobs(ids []string) map[string]bool {
	m.mu.RLock()
	references := m.reapReferences
	vanished := false
	for _, id := range ids {
		if j, ok := m.jobs[id]; ok && workdirVanished(j.Workdir) {
			vanished = true
			break
		}
	}
	m.mu.RUnlock()
	if references == nil || !vanished {
		return nil
	}
	refs, err := references()
	if err != nil {
		return nil
	}
	return refs
}

// A deleted worktree can no longer be resolved by the lease guard, which kept
// its finished jobs forever. Such a job may go unless a lease names it or it
// is a workflow stage whose pipeline may still need the receipt.
func vanishedUnreferenced(j *Job, referenced map[string]bool) bool {
	return referenced != nil && !referenced[j.ID] && !strings.HasPrefix(j.ID, "workflow-") && workdirVanished(j.Workdir)
}

func workdirVanished(dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	_, err := os.Lstat(dir)
	return errors.Is(err, os.ErrNotExist)
}

func pendingWriter(j *Job) bool {
	return (j.External != nil && !j.ResourcesReleased) || j.State == Queued || j.State == Running || j.State == Cancelling || (j.State == Unknown && !j.ResourcesReleased) || (j.State == Failed && j.Error == "mcpshka restarted while job was active")
}

func inProject(work, root string) bool {
	return work == root || strings.HasPrefix(work, strings.TrimRight(root, string(filepath.Separator))+string(filepath.Separator))
}

// Admission and lease CAS share the manager lock. No second native job can
// pass its lease check between observing an idle project and acquiring its lease.
func (m *Manager) WithIdleProject(root string, action func() error) error {
	return m.WithIdleProjects([]string{root}, action)
}

func (m *Manager) WithIdleProjects(roots []string, action func() error) error {
	m.ReconcileUnknown(16)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starting != 0 {
		return failure.RejectedAs(failure.OwnerBusy, "PROJECT_BUSY", "jobs.WithIdleProject", "job admission in progress; nothing was written — repeat the same call shortly", nil)
	}
	for _, j := range m.jobs {
		for _, root := range roots {
			if inProject(j.Workdir, root) && pendingWriter(j) {
				return projectBusy(j)
			}
		}
	}
	return action()
}

// projectBusy is a refusal before any effect: the scope is busy with a named
// job. It was FENCED_UNKNOWN/HOLD_REASON_UNCLASSIFIED before, which read as
// "maybe written" (DIAG ACTIVE-04, MCP-121). It is marked no-effect so the
// mutation receipt is released and the same call (same key) can be repeated:
// as a plain HOLD it left integration_ship's fixed worker key PENDING
// forever (MCP-136).
func projectBusy(j *Job) error {
	e := failure.RejectedAs(failure.OwnerBusy, "PROJECT_BUSY", "jobs.WithIdleProject", "project job still pending: "+j.ID+" is "+string(j.State)+"; nothing was written — wait for it, then repeat the same call", nil)
	e.NextSafeAction = failure.Action{Tool: "job_wait", Arguments: map[string]any{"job_id": j.ID}}
	return e
}

// The callback runs only after fresh, non-signalling process/group verification.
// UNKNOWN remains UNKNOWN; releasing a writer lease does not accept its outcome.
func (m *Manager) WithGoneOwner(root, id string, action func(Snapshot) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starting != 0 {
		return errors.New("job admission in progress: HOLD")
	}
	j, ok := m.jobs[id]
	if !ok || !inProject(j.Workdir, root) {
		return errors.New("owner job not verifiable: HOLD")
	}
	if j.External != nil {
		if _, err := m.observeExternal(j, false); err != nil {
			return err
		}
		if !isTerminal(j.State) || !j.ResourcesReleased {
			return errors.New("external owner not terminal and fenced: HOLD")
		}
	}
	if j.State == Queued || j.State == Running || j.State == Cancelling {
		return errors.New("owner job still active: HOLD")
	}
	for _, other := range m.jobs {
		if other.ID != id && inProject(other.Workdir, root) && pendingWriter(other) {
			return errors.New("other project job pending: HOLD")
		}
	}
	if j.External != nil {
		return action(snapshot(j))
	}
	gone, err := unknownGroupGone(j)
	if err != nil {
		return err
	}
	if !gone {
		return errors.New("owner resources not verifiably gone: HOLD")
	}
	return action(snapshot(j))
}
