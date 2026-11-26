package runnerhub

import (
	"errors"
	"time"
)

// Wake is one attempt to bring a runner up on a site for a job.
type Wake struct {
	Site string    `json:"site"`
	At   time.Time `json:"at"`
	Err  string    `json:"error,omitempty"`
}

// SiteUse is what the balancer knows of a site, derived from the job log.
type SiteUse struct {
	LastWake, LastServed, LastFail time.Time
}

// NoteWake records a wake of site for job id (err: the site refused).
func (h *Hub) NoteWake(id, site string, err error) error {
	return h.update(func(s *state) error {
		j := find(s, id)
		if j == nil {
			return errors.New("runner job not found")
		}
		w := Wake{Site: site, At: h.Now()}
		if err != nil {
			w.Err = err.Error()
			if len(w.Err) > 300 {
				w.Err = w.Err[:300]
			}
		}
		if len(j.Wakes) < 16 {
			j.Wakes = append(j.Wakes, w)
		}
		return nil
	})
}

// GiveUp ends a still-queued job so no late runner takes it; H96 runs it
// instead. false: a runner leased it meanwhile and the job stays off-box.
func (h *Hub) GiveUp(id, why string) (bool, error) {
	ok := false
	err := h.update(func(s *state) error {
		j := find(s, id)
		if j == nil {
			return errors.New("runner job not found")
		}
		if j.State == Queued {
			j.State, j.Error, j.Finished, ok = NoRunner, why, h.Now(), true
		}
		return nil
	})
	return ok, err
}

// SetFallback names the H96 job that runs a given-up job (or why it could not).
func (h *Hub) SetFallback(id, local, errText string) error {
	return h.update(func(s *state) error {
		j := find(s, id)
		if j == nil {
			return errors.New("runner job not found")
		}
		j.Fallback = local
		if errText != "" {
			j.Error += "; H96 fallback failed: " + errText
		}
		return nil
	})
}

// Sites summarizes, per site, the last wake, the last job one of its
// runners finished and the last refused wake.
func (h *Hub) Sites() map[string]SiteUse {
	out := map[string]SiteUse{}
	_, js, _ := h.Snapshot()
	for _, j := range js {
		if j.Site != "" && (j.State == Done || j.State == Failed) {
			u := out[j.Site]
			u.LastServed = later(u.LastServed, j.Finished)
			out[j.Site] = u
		}
		for _, w := range j.Wakes {
			u := out[w.Site]
			u.LastWake = later(u.LastWake, w.At)
			if w.Err != "" {
				u.LastFail = later(u.LastFail, w.At)
			}
			out[w.Site] = u
		}
	}
	return out
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Peek reads one job without writing the state file (job_wait polls it
// every 100 ms); lease expiry is applied to the copy only.
func (h *Hub) Peek(id string) (Job, error) {
	h.mu.Lock()
	s, err := h.load()
	h.mu.Unlock()
	if err != nil {
		return Job{}, err
	}
	h.expire(&s)
	if j := find(&s, id); j != nil {
		return *j, nil
	}
	return Job{}, errors.New("runner job not found")
}

// Open lists queued or running off-box jobs (those with a Workdir) without
// writing the state file.
func (h *Hub) Open() []string {
	h.mu.Lock()
	s, err := h.load()
	h.mu.Unlock()
	if err != nil {
		return nil
	}
	h.expire(&s)
	var ids []string
	for _, j := range s.Jobs {
		if j.Workdir != "" && (j.State == Queued || j.State == Leased) {
			ids = append(ids, j.ID)
		}
	}
	return ids
}
