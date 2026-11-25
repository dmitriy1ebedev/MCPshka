package jobs

// Changed is closed when the job leaves its current phase: admission for a
// queued heavy job, the end for a running one. It lets job_wait sleep until
// something happens instead of reading the job (a systemd scope query each
// time) every 500 ms (MCP-273). nil means there is no local signal — a
// foreign, external or reloaded job — and the caller polls.
func (m *Manager) Changed(id string) <-chan struct{} {
	if foreignFor(id) != nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	j := m.jobs[id]
	if j == nil || j.External != nil {
		return nil
	}
	if j.queueDone != nil && !doneClosed(j.queueDone) {
		return j.queueDone
	}
	return j.done
}
