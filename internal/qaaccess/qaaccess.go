// Package qaaccess binds a temporary public QA URL (Tailscale Funnel) to the
// authorized E2E job that needs it (MCP-089): the URL lives while the job is
// alive, renews by heartbeat up to a hard cap, closes the moment the job ends
// and always records why it closed. Production ports are never exposed.
package qaaccess

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultTTL = 30 * time.Minute
	HardCap    = 2 * time.Hour

	ClosedJobFinished = "JOB_FINISHED"
	ClosedTTL         = "TTL_EXPIRED_NO_HEARTBEAT"
	ClosedHardCap     = "HARD_CAP_REACHED"
	ClosedByOwner     = "CLOSED_BY_OWNER"
)

// productionPorts are services of H96 itself; a QA URL never fronts them.
var productionPorts = map[int]string{22: "ssh", 53: "dns", 80: "http", 443: "https", 1883: "mqtt", 8080: "zigbee2mqtt", 8123: "home-assistant", 1984: "go2rtc", 8090: "torrserver", 8443: "mcpshka-tunnel"}

var (
	ErrProductionPort = errors.New("QA_PORT_IS_PRODUCTION")
	ErrNoJob          = errors.New("QA_ACCESS_REQUIRES_LIVE_JOB")
	ErrActive         = errors.New("QA_ACCESS_ALREADY_OPEN")
)

type Lease struct {
	ID          string    `json:"id"`
	JobID       string    `json:"job_id"`
	Port        int       `json:"port"`
	OpenedAt    time.Time `json:"opened_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	HardUntil   time.Time `json:"hard_until"`
	ClosedAt    time.Time `json:"closed_at,omitempty"`
	CloseReason string    `json:"close_reason,omitempty"`
	// Expiry of the URL is never a failure of the application under test.
	Note string `json:"note,omitempty"`
}

func (l Lease) Remaining(now time.Time) time.Duration {
	if !l.ClosedAt.IsZero() {
		return 0
	}
	return max(0, l.ExpiresAt.Sub(now))
}

// Exposer turns the public URL on and off (tailscale funnel in production).
type Exposer interface {
	Open(port int) error
	Close(port int) error
}

type Manager struct {
	mu      sync.Mutex
	expose  Exposer
	alive   func(jobID string) bool
	now     func() time.Time
	leases  map[string]*Lease
	counter int
}

func New(e Exposer, alive func(string) bool, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{expose: e, alive: alive, now: now, leases: map[string]*Lease{}}
}

func (m *Manager) Open(jobID string, port int, ttl time.Duration) (Lease, error) {
	if name, ok := productionPorts[port]; ok || port <= 0 || port > 65535 {
		return Lease{}, fmt.Errorf("%w: %d %s", ErrProductionPort, port, name)
	}
	if jobID == "" || !m.alive(jobID) {
		return Lease{}, ErrNoJob
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.leases {
		if l.ClosedAt.IsZero() && l.Port == port {
			return *l, ErrActive
		}
	}
	if err := m.expose.Open(port); err != nil {
		return Lease{}, err
	}
	now := m.now().UTC()
	m.counter++
	l := &Lease{ID: fmt.Sprintf("qa-%d-%d", now.Unix(), m.counter), JobID: jobID, Port: port, OpenedAt: now, HardUntil: now.Add(HardCap)}
	l.ExpiresAt = minTime(now.Add(min(ttl, HardCap)), l.HardUntil)
	m.leases[l.ID] = l
	return *l, nil
}

// Sweep renews leases whose job is alive and closes the rest with a reason.
// It runs on every status read and on a ticker, so a finished job never
// leaves a public URL behind.
func (m *Manager) Sweep() []Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	var out []Lease
	for _, l := range m.leases {
		if l.ClosedAt.IsZero() {
			switch {
			case !m.alive(l.JobID):
				m.close(l, now, ClosedJobFinished)
			case !now.Before(l.HardUntil):
				m.close(l, now, ClosedHardCap)
			case !now.Before(l.ExpiresAt):
				m.close(l, now, ClosedTTL)
			default:
				l.ExpiresAt = minTime(now.Add(DefaultTTL), l.HardUntil)
			}
		}
		out = append(out, *l)
	}
	return out
}

func (m *Manager) Close(id string) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[id]
	if !ok {
		return Lease{}, errors.New("unknown QA lease")
	}
	if l.ClosedAt.IsZero() {
		m.close(l, m.now().UTC(), ClosedByOwner)
	}
	return *l, nil
}

func (m *Manager) close(l *Lease, now time.Time, reason string) {
	_ = m.expose.Close(l.Port)
	l.ClosedAt, l.CloseReason = now, reason
	l.Note = "the public URL closed (" + reason + "); this is not a failure of the application under test"
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
