// Package selfheal performs one bounded, durable Docker repair per failure episode.
package selfheal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/statestore"
)

const stateKey = "selfheal/state.json"
const Interval = time.Minute
const Window = time.Hour

type Docker interface {
	RestartContainers() []string
	Status(context.Context, string) (dockerops.Status, error)
	Inspect(context.Context, string) (dockerops.Inspect, error)
	Restart(context.Context, string) (dockerops.Status, error)
}
type Auditor interface{ Log(audit.Event) error }
type Config struct {
	Enabled          bool `json:"enabled"`
	UnhealthyMinutes int  `json:"unhealthy_minutes"`
	VerifyMinutes    int  `json:"verify_minutes"`
	MaxRepairs       int  `json:"max_repairs_per_hour"`
}

func FromEnvironment() (Config, error) {
	c := Config{true, 5, 5, 2}
	if raw := strings.TrimSpace(os.Getenv("MCPSHKA_SELF_HEAL_ENABLED")); raw != "" {
		v, e := strconv.ParseBool(raw)
		if e != nil {
			return c, errors.New("invalid MCPSHKA_SELF_HEAL_ENABLED")
		}
		c.Enabled = v
	}
	for _, v := range []struct {
		name string
		dst  *int
		max  int
	}{
		{"MCPSHKA_SELF_HEAL_UNHEALTHY_MINUTES", &c.UnhealthyMinutes, 1440},
		{"MCPSHKA_SELF_HEAL_VERIFY_MINUTES", &c.VerifyMinutes, 60},
		{"MCPSHKA_SELF_HEAL_MAX_REPAIRS", &c.MaxRepairs, 10},
	} {
		if raw := strings.TrimSpace(os.Getenv(v.name)); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < 1 || n > v.max {
				return c, fmt.Errorf("invalid %s", v.name)
			}
			*v.dst = n
		}
	}
	return c, nil
}

type Repair struct {
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint"`
	Result      string    `json:"result"`
}
type Container struct {
	Failure     string    `json:"failure,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	FirstSeen   time.Time `json:"first_seen,omitempty"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
	Phase       string    `json:"phase,omitempty"`
	Deadline    time.Time `json:"deadline,omitempty"`
	History     []Repair  `json:"history"`
}
type Incident struct {
	At          time.Time `json:"at"`
	Container   string    `json:"container"`
	Fingerprint string    `json:"fingerprint"`
	Result      string    `json:"result"`
}
type State struct {
	Containers map[string]Container `json:"containers"`
	Incidents  []Incident           `json:"incidents"`
}
type Status struct {
	Config
	Active          bool     `json:"active"`
	WindowSeconds   int      `json:"window_seconds"`
	IntervalSeconds int      `json:"interval_seconds"`
	Allowlist       []string `json:"allowlist"`
	State
}
type Service struct {
	db     *statestore.DB
	docker Docker
	audit  Auditor
	config Config
	now    func() time.Time
}

func (s *Service) Enabled() bool { return s != nil && s.config.Enabled }

func New(db *statestore.DB, docker Docker, logger Auditor, c Config) *Service {
	return &Service{db: db, docker: docker, audit: logger, config: c, now: time.Now}
}
func (s *Service) read(ctx context.Context) (State, []byte, error) {
	st := State{Containers: map[string]Container{}, Incidents: []Incident{}}
	if s.db == nil {
		return st, nil, errors.New("self-heal SQLite unavailable")
	}
	raw, e := s.db.GetContext(ctx, stateKey)
	if errors.Is(e, os.ErrNotExist) {
		return st, nil, nil
	}
	if e != nil {
		return st, nil, e
	}
	if e = json.Unmarshal(raw, &st); e != nil || st.Containers == nil || len(st.Containers) > 128 || len(st.Incidents) > 128 {
		return st, nil, errors.New("invalid self-heal state")
	}
	for _, c := range st.Containers {
		if len(c.History) > 32 || (c.Phase == "verifying" && (len(c.History) == 0 || c.Deadline.IsZero())) {
			return st, nil, errors.New("invalid self-heal repair history")
		}
	}
	return st, raw, nil
}
func (s *Service) Status(ctx context.Context) (Status, error) {
	st, _, e := s.read(ctx)
	names := s.docker.RestartContainers()
	return Status{Config: s.config, Active: s.config.Enabled && len(names) > 0, WindowSeconds: 3600, IntervalSeconds: 60, Allowlist: names, State: st}, e
}
func (s *Service) save(st State, old []byte) error {
	raw, e := json.Marshal(st)
	if e != nil {
		return e
	}
	var ok bool
	if old == nil {
		ok, e = s.db.PutIfAbsent(stateKey, raw)
	} else {
		ok, e = s.db.Replace(stateKey, old, raw)
	}
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("self-heal state changed; defer pass")
	}
	return nil
}
func fingerprint(v dockerops.Status, info dockerops.Inspect) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s|%s", info.ID, v.Name, v.Status, v.HealthStatus, v.ExitCode, v.StartedAt, v.FinishedAt, info.RestartPolicy)))
	return hex.EncodeToString(sum[:])
}
func (s *Service) event(st *State, name string, c Container, result string, now time.Time) error {
	st.Incidents = append(st.Incidents, Incident{now, name, c.Fingerprint, result})
	if len(st.Incidents) > 128 {
		st.Incidents = st.Incidents[len(st.Incidents)-128:]
	}
	if s.audit == nil {
		return errors.New("self-heal audit unavailable")
	}
	return s.audit.Log(audit.Event{Time: now, Type: "self_heal", Result: result, Details: map[string]any{"container": name, "fingerprint": c.Fingerprint}})
}

// Step uses bounded inspect calls only for the restart allowlist. CAS reserves
// the attempt before Docker; a lost response can only resume verification.
func (s *Service) Step(ctx context.Context) error {
	if !s.config.Enabled {
		return nil
	}
	names := s.docker.RestartContainers()
	if len(names) > 128 {
		return errors.New("self-heal allowlist exceeds 128 containers")
	}
	for _, name := range names {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := s.stepContainer(ctx, name); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) stepContainer(ctx context.Context, name string) error {
	st, raw, e := s.read(ctx)
	if e != nil {
		return e
	}
	c, known := st.Containers[name]
	if !known && len(st.Containers) >= 128 {
		return errors.New("self-heal retained container capacity reached")
	}
	now := s.now().UTC()
	v, e := s.docker.Status(ctx, name)
	if e != nil {
		// An observation gap cannot count as continuous unhealthy time.
		if c.Phase == "observing" {
			c.FirstSeen = time.Time{}
			c.LastSeen = time.Time{}
			st.Containers[name] = c
			return s.save(st, raw)
		}
		if c.Phase != "verifying" || now.Before(c.Deadline) {
			return nil
		}
	}
	if c.Phase == "verifying" {
		result := ""
		if e == nil && s.serving(ctx, name, v) && !now.After(c.Deadline) {
			result = "recovered"
		} else if !now.Before(c.Deadline) {
			result = "escalated: recovery not verified"
		}
		if result == "" {
			return nil
		}
		c.Phase = result
		c.History[len(c.History)-1].Result = result
		if e = s.event(&st, name, c, result, now); e != nil {
			return e
		}
		st.Containers[name] = c
		return s.save(st, raw)
	}
	if e != nil {
		return nil
	}
	unhealthy := v.Running && !v.Restarting && v.HealthStatus == "unhealthy"
	var info dockerops.Inspect
	// A failed restart leaves its reason in State.Error, even with exit
	// code 0 (25.11 04:46: Zigbee2MQTT's shim died, Docker's restarts failed
	// and it stayed down with exit 0 for 1.5 h). A docker stop leaves it empty.
	failed := v.Status == "exited" && (v.ExitCode != 0 || strings.TrimSpace(v.Error) != "")
	if unhealthy || failed {
		info, e = s.docker.Inspect(ctx, name)
		if e != nil {
			return nil
		}
	}
	crashed := failed && !v.Restarting && info.RestartPolicy != "" && info.RestartPolicy != "no"
	// A running container cut off from its network looks fine to Status;
	// it was reported "cleared" while serving nothing (torrserver 10.11).
	detached := false
	if v.Running && !v.Restarting && !unhealthy {
		if info, e = s.docker.Inspect(ctx, name); e == nil {
			detached = info.Detached
		}
	}
	missingMDNS, mdnsKnown := s.missingMDNS(ctx, name, v)
	if !mdnsKnown && !unhealthy && !crashed && !detached {
		return nil
	}
	if !unhealthy && !crashed && !detached && !missingMDNS {
		if c.Fingerprint == "" {
			return nil
		}
		// A failed repair stays latched until healthy is actually observed.
		if strings.HasPrefix(c.Phase, "escalated") && !(v.Running && (v.HealthStatus == "healthy" || v.HealthStatus == "")) {
			return nil
		}
		if e = s.event(&st, name, c, "cleared", now); e != nil {
			return e
		}
		c.Fingerprint = ""
		c.Failure = ""
		c.Phase = ""
		c.FirstSeen = time.Time{}
		c.LastSeen = time.Time{}
		st.Containers[name] = c
		return s.save(st, raw)
	}
	if strings.HasPrefix(c.Phase, "escalated") {
		return nil
	}
	failure := v
	if missingMDNS {
		failure.HealthStatus = mdnsFailure
	}
	fp := fingerprint(failure, info)
	if c.Phase == "recovered" || c.Fingerprint != fp || c.FirstSeen.IsZero() || now.Before(c.LastSeen) || now.Sub(c.LastSeen) > 2*Interval+30*time.Second {
		c.Fingerprint = fp
		c.FirstSeen = now
		c.Phase = "observing"
		c.Failure = ""
		result := "detected"
		if missingMDNS {
			c.Failure = mdnsFailure
			result += ": " + mdnsFailure
		}
		if e = s.event(&st, name, c, result, now); e != nil {
			return e
		}
	}
	c.LastSeen = now
	if (unhealthy || crashed) && now.Sub(c.FirstSeen) < time.Duration(s.config.UnhealthyMinutes)*time.Minute {
		st.Containers[name] = c
		return s.save(st, raw)
	}
	if name == "go2rtc" && !mdnsRestartDue(v, c, now) {
		st.Containers[name] = c
		return s.save(st, raw)
	}
	count := 0
	for _, r := range c.History {
		if !r.At.Before(now.Add(-Window)) {
			count++
		}
	}
	if count >= s.config.MaxRepairs {
		c.Phase = "escalated: hourly repair limit"
		if e = s.event(&st, name, c, c.Phase, now); e != nil {
			return e
		}
		st.Containers[name] = c
		return s.save(st, raw)
	}
	c.Phase = "verifying"
	c.Deadline = now.Add(time.Duration(s.config.VerifyMinutes) * time.Minute)
	c.History = append(c.History, Repair{now, fp, "verifying"})
	// MaxRepairs is capped at 10; retaining 32 attempts preserves the hourly limit.
	if len(c.History) > 32 {
		c.History = c.History[len(c.History)-32:]
	}
	if e = s.event(&st, name, c, "restart reserved", now); e != nil {
		return e
	}
	st.Containers[name] = c
	if e = s.save(st, raw); e != nil {
		return e
	}
	_, restartErr := s.docker.Restart(ctx, name)
	// Re-read the reservation so another controller's observation cannot be lost.
	st, raw, e = s.read(ctx)
	if e != nil {
		return e
	}
	c = st.Containers[name]
	result := "restart submitted"
	if restartErr != nil {
		result = "escalated: restart failed or outcome unknown"
		c.Phase = result
		c.History[len(c.History)-1].Result = result
	}
	if e = s.event(&st, name, c, result, now); e != nil {
		return e
	}
	st.Containers[name] = c
	return s.save(st, raw)
}

// serving: running and healthy, or running without a healthcheck and with
// every network attached.
func (s *Service) serving(ctx context.Context, name string, v dockerops.Status) bool {
	if !v.Running || v.Restarting {
		return false
	}
	missing, known := s.missingMDNS(ctx, name, v)
	if !known || missing {
		return false
	}
	if v.HealthStatus != "" {
		return v.HealthStatus == "healthy"
	}
	info, err := s.docker.Inspect(ctx, name)
	return err == nil && !info.Detached
}

// Start belongs to the daemon lifetime, never to an MCP client session.
func (s *Service) Start(report func(error)) func() {
	if !s.config.Enabled || len(s.docker.RestartContainers()) == 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pass, stop := context.WithTimeout(ctx, 50*time.Second)
				e := s.Step(pass)
				stop()
				if e != nil && ctx.Err() == nil && report != nil {
					report(e)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
