package executor

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const maxHealthPermits = 512

var errHealthScanFull = errors.New("health permit window full")

// PermitHealth is observation only: an overdue permit never authorizes fencing.
type PermitHealth struct {
	State        string    `json:"state"`
	CapturedAt   time.Time `json:"captured_at"`
	Checked      int       `json:"checked"`
	Pending      int       `json:"pending"`
	Overdue      int       `json:"overdue"`
	Unknown      int       `json:"unknown"`
	Truncated    bool      `json:"truncated"`
	LimitSeconds int64     `json:"limit_seconds"`
	PermitID     string    `json:"permit_id,omitempty"`
	LaunchID     string    `json:"launch_id,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

// Health bounds authority reads. Incomplete scans and legacy records without an
// age stay UNKNOWN; terminal history cannot turn an unobserved permit green.
func (c *Controller) Health(ctx context.Context, now time.Time) (PermitHealth, error) {
	r := PermitHealth{State: "ok", CapturedAt: now, LimitSeconds: 3660}
	if c.db == nil {
		return r, ErrHold
	}
	// Fenced history accumulates (61 records on 23.10): a 64-record window
	// left every scan truncated and the executor UNKNOWN for good. One paged
	// scan replaces a sqlite3 start per record (97 records timed out at load 12).
	seen := 0
	err := c.db.ScanContext(ctx, "executor/permits/", func(k string, raw []byte) error {
		if seen >= maxHealthPermits {
			r.Truncated = true
			return errHealthScanFull
		}
		seen++
		var s Snapshot
		if len(raw) > 8192 || json.Unmarshal(raw, &s) != nil || !validPin(s.Pin) || k != key(s.Pin) || s.Revision == 0 {
			r.Unknown++
			return nil
		}
		r.Checked++
		if s.State == "FENCED" {
			return nil
		}
		if s.State != "ACTIVE" && s.State != "REVOKING" {
			r.Unknown++
			return nil
		}
		r.Pending++
		if s.CreatedAt.IsZero() || s.CreatedAt.After(now.Add(time.Second)) {
			r.Unknown++
			return nil
		}
		if now.Sub(s.CreatedAt) > time.Duration(r.LimitSeconds)*time.Second {
			r.Overdue++
			if r.PermitID == "" {
				r.PermitID = s.Pin.PermitID
				if len(s.Launches) > 0 && idPattern.MatchString(s.Launches[0].ID) {
					r.LaunchID = s.Launches[0].ID
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errHealthScanFull) {
		return r, err
	}
	switch {
	case r.Overdue > 0:
		r.State = "degraded"
	case r.Unknown > 0 || r.Truncated:
		r.State = "unknown"
	}
	return r, nil
}

// HealthAuthority exposes no pins, source, commands, credentials or worker output.
func HealthAuthority(ctx context.Context) (PermitHealth, error) {
	c, err := openAuthority(ctx, true)
	if err != nil {
		return PermitHealth{}, err
	}
	return c.Health(ctx, time.Now().UTC())
}
