package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Runner keeps background brain runs in Dir (one JSON file each) and
// resumes the running ones after a restart.
type Runner struct {
	Dir string
	// LLM is per run: the requested model ("" = the brain endpoint).
	LLM     func(model string) LLM
	Call    Call
	Max     int // concurrent runs; 0 = 2
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	sem     chan struct{}
}

// Summary is a run without its dialogue.
type Summary struct {
	ID       string    `json:"id"`
	Task     string    `json:"task"`
	State    string    `json:"state"`
	Model    string    `json:"model,omitempty"`
	Steps    int       `json:"steps"`
	MaxSteps int       `json:"max_steps"`
	Answer   string    `json:"answer,omitempty"`
	Error    string    `json:"error,omitempty"`
	Updated  time.Time `json:"updated"`
}

func (r *Runner) path(id string) string { return filepath.Join(r.Dir, id+".json") }

func validID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// IDFor makes the run id from the retry key, so a repeated start finds the
// same run instead of starting a second one.
func IDFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "brain-" + hex.EncodeToString(sum[:8])
}

func (r *Runner) save(rec *Record) {
	_ = os.MkdirAll(r.Dir, 0o700)
	raw, _ := json.Marshal(rec)
	tmp := r.path(rec.ID) + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, r.path(rec.ID))
	}
}

// Load reads one run.
func (r *Runner) Load(id string) (*Record, error) {
	if !validID(id) {
		return nil, errors.New("invalid run id")
	}
	raw, err := os.ReadFile(r.path(id))
	if err != nil {
		return nil, errors.New("no such brain run")
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// Start begins (or finds, by key) a background run.
func (r *Runner) Start(key, task, model string, maxSteps int) (Summary, error) {
	if strings.TrimSpace(key) == "" {
		return Summary{}, errors.New("idempotency_key required")
	}
	id := IDFor(key)
	if rec, err := r.Load(id); err == nil {
		return rec.Summary(), nil
	}
	rec, err := New(id, task, model, maxSteps)
	if err != nil {
		return Summary{}, err
	}
	r.save(rec)
	r.launch(rec)
	return rec.Summary(), nil
}

func (r *Runner) launch(rec *Record) {
	r.mu.Lock()
	if r.cancels == nil {
		r.cancels = map[string]context.CancelFunc{}
	}
	if r.sem == nil {
		n := r.Max
		if n <= 0 {
			n = 2
		}
		r.sem = make(chan struct{}, n)
	}
	if _, running := r.cancels[rec.ID]; running {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancels[rec.ID] = cancel
	sem := r.sem
	r.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.cancels, rec.ID)
			r.mu.Unlock()
			cancel()
		}()
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return
		}
		_ = rec.Drive(ctx, r.LLM(rec.Requested), r.Call, r.save)
	}()
}

// Cancel stops a run and marks it cancelled.
func (r *Runner) Cancel(id string) (Summary, error) {
	rec, err := r.Load(id)
	if err != nil {
		return Summary{}, err
	}
	r.mu.Lock()
	cancel := r.cancels[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if rec.State == "running" {
		rec.State, rec.Updated = "cancelled", time.Now().UTC()
		r.save(rec)
	}
	return rec.Summary(), nil
}

// Resume relaunches the runs a restart interrupted.
func (r *Runner) Resume() int {
	n := 0
	for _, s := range r.List() {
		if s.State != "running" {
			continue
		}
		if rec, err := r.Load(s.ID); err == nil {
			r.launch(rec)
			n++
		}
	}
	return n
}

// List is every run, newest first.
func (r *Runner) List() []Summary {
	files, _ := filepath.Glob(filepath.Join(r.Dir, "brain-*.json"))
	out := []Summary{}
	for _, f := range files {
		if rec, err := r.Load(strings.TrimSuffix(filepath.Base(f), ".json")); err == nil {
			out = append(out, rec.Summary())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Summary drops the dialogue.
func (rec *Record) Summary() Summary {
	return Summary{ID: rec.ID, Task: rec.Task, State: rec.State, Model: rec.Model, Steps: len(rec.Steps),
		MaxSteps: rec.MaxSteps, Answer: rec.Answer, Error: rec.Error, Updated: rec.Updated}
}
