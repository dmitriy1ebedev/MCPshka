package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/audit"
)

func (m *Manager) Reap(retention time.Duration, now time.Time) (int, error) {
	if retention < 0 {
		return 0, errors.New("retention must be >= 0")
	}
	cutoff := now.Add(-retention)
	var doomed []string

	m.mu.RLock()
	for id, j := range m.jobs {
		if isTerminal(j.State) && j.Error != "mcpshka restarted while job was active" && !j.FinishedAt.IsZero() && !j.FinishedAt.After(cutoff) {
			doomed = append(doomed, id)
		}
	}
	m.mu.RUnlock()

	referenced := m.reapReferencedJobs(doomed)
	removed := 0
	for _, id := range doomed {
		m.mu.Lock()
		j, ok := m.jobs[id]
		if !ok || !isTerminal(j.State) || (j.External != nil && !j.ResourcesReleased) {
			m.mu.Unlock()
			continue
		}
		if m.leaseGuard != nil && m.leaseGuard(j.Workdir, "") != nil && !vanishedUnreferenced(j, referenced) {
			m.mu.Unlock()
			continue
		}
		if j.External != nil {
			if _, err := m.observeExternal(j, false); err != nil || !isTerminal(j.State) || !j.ResourcesReleased {
				m.mu.Unlock()
				continue
			}
		}
		// Wait proves only leader exit. Keep receipts and private temporary
		// files until the bound process group and scope are proven gone.
		if j.PID > 0 || j.ScopeUnit != "" {
			gone, err := unknownGroupGone(j)
			if err != nil || !gone {
				m.mu.Unlock()
				continue
			}
		}
		delete(m.jobs, id)
		m.mu.Unlock()

		var deleted []byte
		key := "jobs/" + id + "/" + metadataName
		restoreMemory := func() { m.mu.Lock(); m.jobs[id] = j; m.mu.Unlock() }
		if m.database != nil {
			data, err := m.database.Get(key)
			if err != nil {
				restoreMemory()
				return removed, err
			}
			if ok, err := m.database.Delete(key, data); err != nil || !ok {
				restoreMemory()
				return removed, errors.New("job metadata changed during reap")
			}
			deleted = data
		}
		if err := os.RemoveAll(filepath.Join(m.root, id)); err != nil {
			restoreMemory()
			if m.database != nil {
				if restoreErr := m.database.Put(key, deleted); restoreErr != nil {
					return removed, errors.New("job cleanup failed; durable recovery required")
				}
			}
			return removed, err
		}
		removed++
		m.log(audit.Event{Type: "job_reaped", JobID: id, Result: "removed"})
	}
	return removed, nil
}

func (m *Manager) StartReaper(interval, retention time.Duration) func() {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				m.ReconcileUnknown(16)
				// MCP-170: reap disposable compiler scratch only. Never erase
				// authoritative job metadata, stdout/stderr or recovery receipts
				// in a periodic maintenance sweep. Retention is 72/96 hours (owner decision 23.11, MCP-274).
				_, _ = m.ArtifactGC(now, true, "")
				_ = m.maintainGoCache(context.Background())
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (m *Manager) loadExisting() error {
	var names []string
	scanned := map[string][]byte{}
	if m.database != nil {
		err := m.database.Scan("jobs/", func(key string, payload []byte) error {
			if !strings.HasSuffix(key, "/"+metadataName) {
				return errors.New("unexpected job state key")
			}
			name := strings.TrimSuffix(strings.TrimPrefix(key, "jobs/"), "/"+metadataName)
			if name == "" || strings.Contains(name, "/") {
				return errors.New("invalid persisted job path")
			}
			names = append(names, name)
			scanned[name] = payload
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		entries, err := os.ReadDir(m.root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
	}
	for _, name := range names {
		path := filepath.Join(m.root, name, metadataName)
		var b []byte
		var err error
		if m.database != nil {
			b = scanned[name]
		} else {
			b, err = os.ReadFile(path)
		}
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		var s Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		if s.ID == "" || s.ID != name {
			return fmt.Errorf("invalid persisted job id in %s", path)
		}
		if s.State == Running || s.State == Queued || s.State == Cancelling {
			s.State = Unknown
			s.Error = "mcpshka restarted while job was active: outcome and process ownership require reconciliation"
			if s.Command == "mcpshka-llm" || s.Command == "mcpshka-fixes" {
				s.State = Failed
				s.ResourcesReleased = true
				s.Error = "native worker interrupted by restart; inspect original child jobs and fixes_review before continuing"
				s.FinishedAt = time.Now().UTC()
			}
			if s.State == Unknown {
				s.FinishedAt = time.Time{}
			}
			if err := m.persist(s); err != nil {
				return err
			}
		}
		if s.CommandClass == "" {
			s.CommandClass = m.policy.ClassifyCommand(s.Command, s.Args)
		}
		m.jobs[s.ID] = &Job{
			SessionOwner: s.SessionOwner, External: s.External, ID: s.ID, Command: s.Command, CommandClass: s.CommandClass,
			Args:    append([]string(nil), s.Args...),
			Workdir: s.Workdir, State: s.State, PID: s.PID, ExitCode: s.ExitCode,
			BootID: s.BootID, StartTicks: s.StartTicks, ResourcesReleased: s.ResourcesReleased, ReconciledAt: s.ReconciledAt,
			Error: s.Error, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
			TimeoutSeconds: s.TimeoutSeconds, OutputLimitBytes: s.OutputLimitBytes,
			OutputStoredBytes: s.OutputStoredBytes, OutputDroppedBytes: s.OutputDroppedBytes,
			LastOutputAt: s.LastOutputAt, QuietSeconds: s.QuietSeconds,
			MemoryLimitBytes: s.MemoryLimitBytes, CPUTimeLimitSeconds: s.CPUTimeLimitSeconds,
			ProcessLimit: s.ProcessLimit, CPUQuotaPercent: s.CPUQuotaPercent,
			ScopeUnit: s.ScopeUnit, ResourceViolation: s.ResourceViolation,
			stdoutPath: filepath.Join(m.root, s.ID, "stdout.log"),
			stderrPath: filepath.Join(m.root, s.ID, "stderr.log"),
		}
	}
	return nil
}

func (m *Manager) persist(s Snapshot) error {
	dir := filepath.Join(m.root, s.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if m.database != nil {
		return m.database.Put("jobs/"+s.ID+"/"+metadataName, b)
	}
	tmp := filepath.Join(dir, metadataName+".tmp")
	final := filepath.Join(dir, metadataName)
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (m *Manager) log(e audit.Event) {
	if m.audit != nil {
		_ = m.audit.Log(e)
	}
}

type outputQuota struct {
	mu           sync.Mutex
	limit        int64
	stored       int64
	dropped      int64
	lastOutputAt time.Time
}

func (q *outputQuota) stats() (int64, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stored, q.dropped
}

func (q *outputQuota) lastActivity() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastOutputAt
}

type quotaWriter struct {
	dst   io.Writer
	quota *outputQuota
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	w.quota.mu.Lock()
	defer w.quota.mu.Unlock()
	w.quota.lastOutputAt = time.Now().UTC()

	remaining := w.quota.limit - w.quota.stored
	keep := len(p)
	if remaining <= 0 {
		keep = 0
	} else if int64(keep) > remaining {
		keep = int(remaining)
	}

	if keep > 0 {
		n, err := w.dst.Write(p[:keep])
		w.quota.stored += int64(n)
		if err != nil {
			return n, err
		}
		if n != keep {
			return n, io.ErrShortWrite
		}
	}
	if keep < len(p) {
		w.quota.dropped += int64(len(p) - keep)
	}
	return len(p), nil
}
