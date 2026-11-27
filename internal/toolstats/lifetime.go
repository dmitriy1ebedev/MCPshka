package toolstats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lifetime totals survive restarts (MCP-151): a window since the last start
// cannot tell a dead tool from a quiet day; the ledger can. Still no
// arguments or results — names, counts and the last use only.
type lifetime struct {
	Calls  int       `json:"calls"`
	Errors int       `json:"errors"`
	Last   time.Time `json:"last,omitempty"`
	// Recovered counts failures whose next call of the same tool succeeded;
	// Failing remembers that the last call failed.
	Recovered int  `json:"recovered,omitempty"`
	Failing   bool `json:"failing,omitempty"`
}

type ledger struct {
	Since time.Time            `json:"since"`
	Tools map[string]*lifetime `json:"tools"`
}

// Persist loads the ledger at path (or starts one) and saves it every
// interval; the returned stop saves once more. A damaged file starts a new
// ledger instead of blocking the server.
func (s *Stats) Persist(path string, every time.Duration) (stop func()) {
	s.mu.Lock()
	s.path = path
	s.ledger = ledger{Since: s.started, Tools: map[string]*lifetime{}}
	if raw, err := os.ReadFile(path); err == nil {
		var l ledger
		if json.Unmarshal(raw, &l) == nil && !l.Since.IsZero() && l.Tools != nil {
			s.ledger = l
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = s.save()
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		_ = s.save()
	}
}

func (s *Stats) save() error {
	s.mu.Lock()
	if s.path == "" || !s.dirty {
		s.mu.Unlock()
		return nil
	}
	raw, err := json.Marshal(s.ledger)
	path := s.path
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tool-usage-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// recordLifetime is called with s.mu held.
func (s *Stats) recordLifetime(tool string, end time.Time, failed bool) {
	if s.ledger.Tools == nil {
		return
	}
	l := s.ledger.Tools[tool]
	if l == nil {
		l = &lifetime{}
		s.ledger.Tools[tool] = l
	}
	l.Calls++
	if failed {
		l.Errors++
	} else if l.Failing {
		l.Recovered++
	}
	l.Failing = failed
	if end.After(l.Last) {
		l.Last = end
	}
	s.dirty = true
}

// LedgerSince is when lifetime counting began (zero: not persisted).
func (s *Stats) LedgerSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ledger.Tools == nil {
		return time.Time{}
	}
	return s.ledger.Since
}

// MinHistory is how many lifetime calls a tool needs before its failure
// share says anything: one error never marks a tool.
const MinHistory = 20

// History is the tool's lifetime calls and errors (zero without a ledger).
func (s *Stats) History(tool string) (calls, errors int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.ledger.Tools[tool]; l != nil {
		return l.Calls, l.Errors
	}
	return 0, 0
}

// Unreliable: at least MinHistory calls and half of them failed.
func Unreliable(calls, errors int) bool { return calls >= MinHistory && errors*2 >= calls }

// Rank orders tools of equal relevance: 1 for an unreliable one.
func Rank(calls, errors int) int {
	if Unreliable(calls, errors) {
		return 1
	}
	return 0
}

// FailureNote words the history of an unreliable tool; empty otherwise.
func FailureNote(calls, errors int) string {
	if !Unreliable(calls, errors) {
		return ""
	}
	return fmt.Sprintf("failed %d of %d calls since the usage ledger began (tool_usage); check its prerequisites before relying on it", errors, calls)
}

// Rate is done/of rounded to three places; nil without a denominator, so a
// tool nobody called never reads as 100%.
func Rate(done, of int) *float64 {
	if of <= 0 {
		return nil
	}
	r := float64(done*1000/of) / 1000
	return &r
}

// Overall sums the lifetime ledger of every tool.
type Overall struct {
	Calls        int      `json:"calls"`
	Errors       int      `json:"errors"`
	SuccessRate  *float64 `json:"success_rate,omitempty"`
	Recovered    int      `json:"recovered"`
	RecoveryRate *float64 `json:"recovery_rate,omitempty"`
	Unreliable   []string `json:"unreliable,omitempty"`
}

func Summarize(usage []Usage) Overall {
	var o Overall
	for _, u := range usage {
		o.Calls += u.TotalCalls
		o.Errors += u.TotalErrors
		o.Recovered += u.Recovered
		if Unreliable(u.TotalCalls, u.TotalErrors) {
			o.Unreliable = append(o.Unreliable, u.Tool)
		}
	}
	o.SuccessRate, o.RecoveryRate = Rate(o.Calls-o.Errors, o.Calls), Rate(o.Recovered, o.Errors)
	return o
}
