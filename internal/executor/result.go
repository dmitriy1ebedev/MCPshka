package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"
	"unicode/utf8"
)

type ExecutionResult struct {
	Pin        Pin       `json:"pin"`
	LaunchID   string    `json:"launch_id"`
	Identity   Identity  `json:"identity"`
	ExitCode   int       `json:"exit_code"`
	WaitFailed bool      `json:"wait_failed"`
	Output     string    `json:"output"`
	ObservedAt time.Time `json:"observed_at"`
	State      string    `json:"state"`
	Trust      string    `json:"trust"`
}

func resultKey(p Pin) string { return "executor/results/" + p.PermitID + ".json" }
func validResult(r ExecutionResult, s Snapshot) bool {
	if r.Pin != s.Pin || !idPattern.MatchString(r.LaunchID) || r.ExitCode < -1 || r.ExitCode > 255 || (len(r.Output) > 8192 || !utf8.ValidString(r.Output)) || r.ObservedAt.IsZero() || r.ObservedAt.After(time.Now().Add(time.Minute)) || r.State != "EXITED_UNFENCED" || r.Trust != "UNTRUSTED" {
		return false
	}
	for _, l := range s.Launches {
		if l.ID == r.LaunchID && l.Identity != nil && *l.Identity == r.Identity && (l.State == "REGISTERED" || l.State == "FENCED") {
			return true
		}
	}
	return false
}

// Result is an observation. Only a verified fence makes its outcome terminal.
func (c *Controller) recordResult(ctx context.Context, r ExecutionResult) error {
	s, _, e := c.read(ctx, r.Pin)
	if e != nil {
		return e
	}
	if s.State != "ACTIVE" || !validResult(r, s) {
		return ErrHold
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	ok, e := c.db.PutIfAbsent(resultKey(r.Pin), b)
	if e != nil {
		return e
	}
	if !ok {
		return ErrHold
	}
	return nil
}
func (c *Controller) attachResult(ctx context.Context, s *Snapshot) error {
	b, e := c.db.GetContext(ctx, resultKey(s.Pin))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	var r ExecutionResult
	if json.Unmarshal(b, &r) != nil || !validResult(r, *s) {
		return ErrHold
	}
	if s.State == "FENCED" {
		r.State = "FAILED"
		if r.ExitCode == 0 && !r.WaitFailed {
			r.State = "SUCCEEDED"
		}
	}
	if s.State != "FENCED" {
		s.NextAction = "canary-fence " + s.Pin.PermitID + " " + r.LaunchID
	}
	s.Result = &r
	return nil
}
