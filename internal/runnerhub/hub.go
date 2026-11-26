// Package runnerhub hands heavy jobs to pull runners outside H96 — the model
// of GitHub Actions self-hosted runners and GitLab Runner: the runner calls
// MCPshka over HTTPS (no inbound port, no SSH), registers once with a
// one-time token the owner issued behind Face ID, then long-polls for one job
// at a time under a lease it renews with heartbeats. A runner that vanishes or
// sleeps (GigaIDE Cloud after 1 h idle) loses the lease and the job goes back
// to the queue. Results come back as UNTRUSTED evidence with a SHA-256.
package runnerhub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	TokenTTL     = time.Hour
	LeaseTTL     = 90 * time.Second
	MaxAttempts  = 3
	maxJobs      = 50
	maxRunners   = 16
	maxOutput    = 256 << 10
	DefaultWait  = 10 * time.Minute // queued longer than this with no runner → NO_RUNNER
	stateVersion = 1
)

// Job states.
const (
	Queued   = "QUEUED"
	Leased   = "RUNNING"
	Done     = "DONE"
	Failed   = "FAILED"
	NoRunner = "NO_RUNNER"
)

type token struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
	// Site marks a token MCPshka minted itself to wake a CI site or cloud
	// IDE (MintEphemeral): its runner is ephemeral and named after the site.
	Site string `json:"site,omitempty"`
}

// Runner is a registered worker. KeyHash never leaves the hub.
type Runner struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	KeyHash  string            `json:"key_hash,omitempty"`
	Created  time.Time         `json:"created"`
	LastSeen time.Time         `json:"last_seen"`
	Caps     map[string]string `json:"caps,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	Job      string            `json:"job,omitempty"`
	// Site: an ephemeral runner a CI run or cloud IDE started on MCPshka's
	// wake (github, gitverse, moshub, gigaide); dropped once it goes quiet.
	Site string `json:"site,omitempty"`
}

// Job is one unit of work: a pinned commit of a project and a fixed argv.
type Job struct {
	ID         string    `json:"id"`
	Project    string    `json:"project"`
	Commit     string    `json:"commit"`
	Kind       string    `json:"kind"`
	Argv       []string  `json:"argv"`
	Needs      string    `json:"needs"` // capability the runner must report, e.g. go
	Timeout    int       `json:"timeout_seconds"`
	State      string    `json:"state"`
	Runner     string    `json:"runner,omitempty"`
	LeaseUntil time.Time `json:"lease_until,omitempty"`
	Attempts   int       `json:"attempts"`
	Created    time.Time `json:"created"`
	Finished   time.Time `json:"finished,omitempty"`
	ExitCode   int       `json:"exit_code"`
	Output     string    `json:"output,omitempty"`
	OutputSHA  string    `json:"output_sha256,omitempty"`
	Error      string    `json:"error,omitempty"`
	Trust      string    `json:"trust"`
	KeyHash    string    `json:"idem,omitempty"` // idempotency key digest: a retried call returns the same job
	// Off-box routing (internal/offbox, sites.go): the site whose runner
	// leased the job, the sites woken for it, and the H96 job that took over.
	Site     string `json:"site,omitempty"`
	Wakes    []Wake `json:"wakes,omitempty"`
	Workdir  string `json:"workdir,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

type state struct {
	Version     int         `json:"version"`
	CloudBudget CloudBudget `json:"cloud_budget"`
	Tokens      []token     `json:"tokens,omitempty"`
	Runners     []Runner    `json:"runners,omitempty"`
	Jobs        []Job       `json:"jobs,omitempty"`
}

// Hub is the durable runner registry and job queue (one JSON file, 0600).
type Hub struct {
	Dir  string
	Now  func() time.Time
	mu   sync.Mutex
	wake chan struct{}
}

func New(dir string) *Hub { return &Hub{Dir: dir, Now: time.Now, wake: make(chan struct{})} }

// Default is the production hub shared by the runner tools and phase_gate.
var Default = New("/var/lib/mcpshka/runners")

// PassedOn reports a whole-module go-test of commit in project that passed
// on a runner: phase_gate counts it like a sealed test, so heavy checks can
// leave H96.
func (h *Hub) PassedOn(project, commit string) (string, bool) {
	if _, err := os.Stat(h.path()); err != nil {
		return "", false
	}
	_, js, _ := h.Snapshot()
	for _, j := range js {
		if j.Project == filepath.Clean(project) && j.Commit == commit && j.Kind == "go-test" && j.State == Done && slices.Equal(j.Argv[3:], []string{"./..."}) {
			return fmt.Sprintf("runner %s: go test ./... passed on %s (%s)", j.Runner, commit[:12], j.ID), true
		}
	}
	return "", false
}

func (h *Hub) path() string { return filepath.Join(h.Dir, "runners.json") }

// BundlePath is where the job's git bundle lives.
func (h *Hub) BundlePath(id string) string { return filepath.Join(h.Dir, "jobs", id+".bundle") }

func (h *Hub) load() (state, error) {
	var s state
	raw, err := os.ReadFile(h.path())
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: stateVersion}, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("runner state unreadable: %w", err)
	}
	return s, nil
}

func (h *Hub) save(s state) error {
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := h.path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	keepOwner(tmp, h.Dir)
	return os.Rename(tmp, h.path())
}

// update runs fn on the loaded state under the lock, expires leases and
// tokens, and saves.
func (h *Hub) update(fn func(*state) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	lock, err := h.lockDisk()
	if err != nil {
		return err
	}
	defer lock.Close()
	s, err := h.load()
	if err != nil {
		return err
	}
	h.expire(&s)
	if err := fn(&s); err != nil {
		return err
	}
	return h.save(s)
}

func (h *Hub) expire(s *state) {
	now := h.Now()
	s.Tokens = slices.DeleteFunc(s.Tokens, func(t token) bool { return now.After(t.Expires) })
	s.Runners = slices.DeleteFunc(s.Runners, func(r Runner) bool {
		return r.Site != "" && r.Job == "" && now.Sub(r.LastSeen) > EphemeralQuiet
	})
	for i := range s.Jobs {
		j := &s.Jobs[i]
		switch {
		case j.State == Leased && now.After(j.LeaseUntil):
			// The runner vanished or slept: back to the queue, or give up.
			h.releaseRunner(s, j.Runner)
			j.Runner = ""
			if j.Attempts >= MaxAttempts {
				j.State, j.Error, j.Finished = Failed, "runner lost the lease "+fmt.Sprint(j.Attempts)+" times", now
			} else {
				j.State = Queued
			}
		case j.State == Queued && now.Sub(j.Created) > DefaultWait*time.Duration(j.Attempts+1):
			j.State, j.Error, j.Finished = NoRunner, "no runner took the job; run it on H96 with project_test", now
		}
	}
}

func (h *Hub) releaseRunner(s *state, id string) {
	for i := range s.Runners {
		if s.Runners[i].ID == id {
			s.Runners[i].Job = ""
		}
	}
}

func (h *Hub) notify() {
	h.mu.Lock()
	close(h.wake)
	h.wake = make(chan struct{})
	h.mu.Unlock()
}

func secret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func digest(v string) string {
	s := sha256.Sum256([]byte(v))
	return hex.EncodeToString(s[:])
}

// NewToken issues a one-time registration token (shown once to the owner).
func (h *Hub) NewToken() (string, time.Time, error) {
	tok := "mrt_" + secret()
	exp := h.Now().Add(TokenTTL)
	err := h.update(func(s *state) error {
		if len(s.Tokens) >= 8 {
			s.Tokens = s.Tokens[1:]
		}
		s.Tokens = append(s.Tokens, token{Hash: digest(tok), Expires: exp})
		return nil
	})
	return tok, exp, err
}

// EphemeralTTL bounds a wake token: a CI run that has not registered by then
// never will. EphemeralQuiet drops its runner after the run ended.
const (
	EphemeralTTL   = 20 * time.Minute
	EphemeralQuiet = 5 * time.Minute
)

var siteRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}$`)

// MintEphemeral issues a one-time token for a runner MCPshka itself wakes on
// a site (forge_ci mode=runner). No Face ID: the site already holds the code,
// the runner only executes fixed profiles of pinned commits, and its results
// stay UNTRUSTED evidence.
func (h *Hub) MintEphemeral(site string) (string, time.Time, error) {
	if !siteRE.MatchString(site) {
		return "", time.Time{}, errors.New("site: lowercase name")
	}
	tok := "mrt_" + secret()
	exp := h.Now().Add(EphemeralTTL)
	err := h.update(func(s *state) error {
		if len(s.Tokens) >= 16 {
			s.Tokens = s.Tokens[1:]
		}
		s.Tokens = append(s.Tokens, token{Hash: digest(tok), Expires: exp, Site: site})
		return nil
	})
	return tok, exp, err
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`)

// Register consumes a token and returns the runner id and its secret key.
func (h *Hub) Register(tok, name string) (string, string, error) {
	if !nameRE.MatchString(name) {
		return "", "", errors.New("runner name: letters, digits, . _ - (≤48)")
	}
	key := "mrk_" + secret()
	id := "r-" + secret()[:12]
	err := h.update(func(s *state) error {
		i := slices.IndexFunc(s.Tokens, func(t token) bool { return t.Hash == digest(tok) })
		if i < 0 {
			return errors.New("registration token unknown, used or expired")
		}
		site := s.Tokens[i].Site
		s.Tokens = slices.Delete(s.Tokens, i, i+1)
		active := 0
		for _, r := range s.Runners {
			if !r.Disabled {
				active++
			}
		}
		if active >= maxRunners {
			return errors.New("too many runners; disable one on the Runners page")
		}
		now := h.Now()
		s.Runners = append(s.Runners, Runner{ID: id, Name: name, KeyHash: digest(key), Created: now, LastSeen: now, Site: site})
		return nil
	})
	return id, key, err
}

// Auth resolves a runner key to an enabled runner.
func (h *Hub) Auth(key string) (Runner, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, err := h.load()
	if err != nil {
		return Runner{}, err
	}
	for _, r := range s.Runners {
		if r.KeyHash == digest(key) && !r.Disabled {
			return r, nil
		}
	}
	return Runner{}, ErrRefused
}

// ErrRefused is the only answer that means «this key will never work»: an
// unknown or disabled runner. Anything else (an unreadable registry, a full
// disk) is the server's trouble and must not make the agent give up.
var ErrRefused = errors.New("runner key unknown or disabled")

// Poll waits up to wait for a job this runner can run and leases it.
func (h *Hub) Poll(runnerID string, caps map[string]string, wait time.Duration) (*Job, error) {
	deadline := h.Now().Add(wait)
	for {
		h.mu.Lock()
		wake := h.wake // taken before the check: an Enqueue in between still wakes us
		h.mu.Unlock()
		var got *Job
		err := h.update(func(s *state) error {
			ri := slices.IndexFunc(s.Runners, func(r Runner) bool { return r.ID == runnerID })
			if ri < 0 || s.Runners[ri].Disabled {
				return ErrRefused
			}
			r := &s.Runners[ri]
			r.LastSeen, r.Caps = h.Now(), boundedCaps(caps)
			if r.Job != "" {
				return nil // one job at a time
			}
			for i := range s.Jobs {
				j := &s.Jobs[i]
				if j.State != Queued || !NeedsMet(j.Needs, r.Caps) {
					continue
				}
				j.State, j.Runner, j.LeaseUntil, j.Site = Leased, r.ID, h.Now().Add(LeaseTTL), r.Site
				j.Attempts++
				r.Job = j.ID
				c := *j
				got = &c
				return nil
			}
			return nil
		})
		if err != nil || got != nil || !h.Now().Before(deadline) {
			return got, err
		}
		select {
		case <-wake:
		case <-time.After(min(deadline.Sub(h.Now()), 15*time.Second)):
		}
	}
}

func boundedCaps(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if len(out) >= 16 || len(k) > 16 || len(v) > 64 {
			continue
		}
		out[k] = v
	}
	return out
}

// Heartbeat renews the lease; cancel=true tells the runner to stop.
func (h *Hub) Heartbeat(runnerID, jobID string) (bool, error) {
	cancel := false
	err := h.update(func(s *state) error {
		j := find(s, jobID)
		if j == nil || j.Runner != runnerID || j.State != Leased {
			cancel = true
			return nil
		}
		j.LeaseUntil = h.Now().Add(LeaseTTL)
		for i := range s.Runners {
			if s.Runners[i].ID == runnerID {
				s.Runners[i].LastSeen = h.Now()
			}
		}
		return nil
	})
	return cancel, err
}

// Result records the outcome of a leased job.
func (h *Hub) Result(runnerID, jobID string, exit int, output, errText string) error {
	if len(output) > maxOutput {
		output = output[len(output)-maxOutput:]
	}
	return h.update(func(s *state) error {
		j := find(s, jobID)
		if j == nil || j.Runner != runnerID || j.State != Leased {
			return errors.New("job is not leased to this runner")
		}
		j.ExitCode, j.Output, j.OutputSHA, j.Finished = exit, output, digest(output), h.Now()
		j.Error = errText
		if len(j.Error) > 1024 {
			j.Error = j.Error[:1024]
		}
		j.State = Done
		if exit != 0 || errText != "" {
			j.State = Failed
		}
		h.releaseRunner(s, runnerID)
		_ = os.Remove(h.BundlePath(j.ID))
		return nil
	})
}

// CancelledPrefix starts the Error of a job stopped by job_cancel.
const CancelledPrefix = "cancelled by job_cancel"

// Cancel stops a queued, leased or given-up job (MCP-292): it becomes
// FAILED with CancelledPrefix, the runner is released and its next
// heartbeat says stop. A finished job is returned unchanged.
func (h *Hub) Cancel(id string) (Job, error) {
	var out Job
	err := h.update(func(s *state) error {
		j := find(s, id)
		if j == nil {
			return fmt.Errorf("runner job %s not found", id)
		}
		switch j.State {
		case Queued, Leased, NoRunner:
			if j.State == Leased && j.Runner != "" {
				h.releaseRunner(s, j.Runner)
			}
			j.State, j.Error, j.Finished = Failed, CancelledPrefix, h.Now()
			_ = os.Remove(h.BundlePath(j.ID))
		}
		out = *j
		return nil
	})
	return out, err
}

func find(s *state, id string) *Job {
	for i := range s.Jobs {
		if s.Jobs[i].ID == id {
			return &s.Jobs[i]
		}
	}
	return nil
}

// Enqueue adds a job whose bundle is already at BundlePath(job.ID).
func (h *Hub) Enqueue(j Job) (Job, error) {
	j.State, j.Created, j.Trust = Queued, h.Now(), "UNTRUSTED"
	createdID := j.ID
	err := h.update(func(s *state) error {
		for _, old := range s.Jobs {
			if j.KeyHash != "" && old.KeyHash == j.KeyHash {
				if old.Project != j.Project || old.Commit != j.Commit || old.Kind != j.Kind || !slices.Equal(old.Argv, j.Argv) {
					return errors.New("runner retry payload conflict")
				}
				j = old
				return nil
			}
		}
		for len(s.Jobs) >= maxJobs {
			n := slices.IndexFunc(s.Jobs, func(old Job) bool { return old.State == Done || old.State == Failed || old.State == NoRunner })
			if n < 0 {
				return errors.New("runner queue full; active jobs preserved")
			}
			_ = os.Remove(h.BundlePath(s.Jobs[n].ID))
			s.Jobs = append(s.Jobs[:n], s.Jobs[n+1:]...)
		}
		s.Jobs = append(s.Jobs, j)
		return nil
	})
	if createdID != j.ID || err != nil {
		_ = os.Remove(h.BundlePath(createdID))
	}
	if err == nil {
		h.notify()
	}
	return j, err
}

// ByKey returns the job an earlier call with this idempotency key created.
func (h *Hub) ByKey(key string) (Job, bool) {
	var out Job
	found := false
	_ = h.update(func(s *state) error {
		for _, j := range s.Jobs {
			if j.KeyHash != "" && j.KeyHash == digest(key) {
				out, found = j, true
			}
		}
		return nil
	})
	return out, found
}

// KeyDigest is the stored form of an idempotency key.
func KeyDigest(key string) string { return digest(key) }

// NewJobID returns a fresh job id.
func NewJobID() string { return "rj-" + secret()[:16] }

// Job returns one job.
func (h *Hub) Job(id string) (Job, error) {
	var out Job
	err := h.update(func(s *state) error {
		j := find(s, id)
		if j == nil {
			return errors.New("runner job not found")
		}
		out = *j
		return nil
	})
	return out, err
}

// Snapshot lists runners (without key hashes) and the newest jobs first.
func (h *Hub) Snapshot() ([]Runner, []Job, error) {
	var rs []Runner
	var js []Job
	err := h.update(func(s *state) error {
		for _, r := range s.Runners {
			r.KeyHash = ""
			rs = append(rs, r)
		}
		js = append(js, s.Jobs...)
		return nil
	})
	sort.Slice(js, func(a, b int) bool { return js[a].Created.After(js[b].Created) })
	for i := range js {
		if len(js[i].Output) > 4096 {
			js[i].Output = js[i].Output[len(js[i].Output)-4096:]
		}
	}
	return rs, js, err
}

// Disable switches a runner off; its job, if any, returns to the queue.
func (h *Hub) Disable(id string) error {
	return h.update(func(s *state) error {
		for i := range s.Runners {
			if s.Runners[i].ID != id {
				continue
			}
			s.Runners[i].Disabled = true
			if jid := s.Runners[i].Job; jid != "" {
				if j := find(s, jid); j != nil && j.State == Leased {
					j.State, j.Runner = Queued, ""
				}
				s.Runners[i].Job = ""
			}
			return nil
		}
		return errors.New("runner not found")
	})
}

// Fit explains why no enabled runner seen in the last 15 min can take a job
// with these needs ("" when one can), so the caller learns it at once instead
// of after DefaultWait as NO_RUNNER.
func (h *Hub) Fit(needs string) string {
	var rs []Runner
	if _, err := os.Stat(h.path()); err == nil {
		rs, _, _ = h.Snapshot()
	}
	var lack []string
	live := 0
	for _, r := range rs {
		if r.Disabled || h.Now().Sub(r.LastSeen) > 2*time.Minute {
			continue
		}
		live++
		if NeedsMet(needs, r.Caps) {
			return ""
		}
		for _, n := range strings.Fields(needs) {
			if r.Caps[n] == "" && !slices.Contains(lack, n) {
				lack = append(lack, n)
			}
		}
	}
	if live == 0 {
		return "no runner seen in the last 15 min (open the cloud IDE: its autostart brings the runner back)"
	}
	return "live runners lack " + strings.Join(lack, ", ") + " (runner agent v3 fetches sqlite3/PyYAML itself after a restart)"
}
