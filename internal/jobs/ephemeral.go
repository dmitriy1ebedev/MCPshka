package jobs

import (
	"errors"
	"time"
)

type ephemeralReceipt struct {
	body     []byte
	group    string
	expires  time.Time
	validate func() error
}

func wipeReceipt(v *ephemeralReceipt) {
	for i := range v.body {
		v.body[i] = 0
	}
	v.body = nil
}
func (m *Manager) InvalidateEphemeralGroup(group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, v := range m.ephemeral {
		if v.group == group {
			wipeReceipt(v)
			delete(m.ephemeral, id)
		}
	}
}

// Output exists only in bounded volatile memory. Journal/argv contain no facts;
// restart/expiry/source revision change requires a fresh read-only observation.
func (m *Manager) StoreEphemeralReceipt(command string, args []string, workdir, output, group string, validate func() error) (Snapshot, error) {
	if command != "mcpshka-knowledge-view" || len(output) > 32768 || group != "knowledge" || validate == nil {
		return Snapshot{}, errors.New("invalid ephemeral receipt")
	}
	j, e := m.StoreReceipt(command, args, workdir, "")
	if e != nil {
		return j, e
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return j, errors.New("manager closing")
	}
	if m.ephemeral == nil {
		m.ephemeral = map[string]*ephemeralReceipt{}
	}
	for id, v := range m.ephemeral {
		if time.Now().After(v.expires) {
			wipeReceipt(v)
			delete(m.ephemeral, id)
		}
	}
	if len(m.ephemeral) >= 32 {
		m.mu.Unlock()
		return j, errors.New("ephemeral view capacity reached")
	}
	v := &ephemeralReceipt{body: []byte(output), group: group, expires: time.Now().Add(2 * time.Minute), validate: validate}
	m.ephemeral[j.ID] = v
	m.mu.Unlock()
	if e = validate(); e != nil {
		m.mu.Lock()
		if m.ephemeral[j.ID] == v {
			wipeReceipt(v)
			delete(m.ephemeral, j.ID)
		}
		m.mu.Unlock()
		return j, e
	}
	return j, nil
}
func (m *Manager) readEphemeral(id, stream string, cursor int64, limit int, state State) (Output, error) {
	if stream == "stderr" {
		return Output{JobID: id, State: state, Stream: stream}, nil
	}
	if stream != "" && stream != "stdout" {
		return Output{}, errors.New("invalid stream")
	}
	if cursor < 0 {
		return Output{}, errors.New("invalid cursor")
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	if limit > MaxReadLimit {
		limit = MaxReadLimit
	}
	m.mu.RLock()
	v := m.ephemeral[id]
	m.mu.RUnlock()
	if v == nil {
		return Output{}, errors.New("temporary evidence unavailable; repeat read-only observation")
	}
	e := v.validate()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ephemeral[id] != v || e != nil || time.Now().After(v.expires) {
		if m.ephemeral[id] == v {
			wipeReceipt(v)
			delete(m.ephemeral, id)
		}
		return Output{}, errors.New("temporary evidence expired or source changed; repeat read-only observation")
	}
	total := int64(len(v.body))
	if cursor > total {
		cursor = total
	}
	end := min(total, cursor+int64(limit))
	return Output{JobID: id, State: state, Stream: "stdout", Cursor: cursor, NextCursor: end, TotalBytes: total, Truncated: end < total, Text: string(v.body[cursor:end])}, nil
}
