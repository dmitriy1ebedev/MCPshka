package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"time"
)

const maxStalePermits = 32

type StalePermitPin struct {
	Pin          Pin    `json:"pin"`
	RecordSHA256 string `json:"record_sha256"`
}
type StalePermitPlan struct {
	Permits []StalePermitPin `json:"permits"`
}
type StalePermitPreview struct {
	Plan    StalePermitPlan `json:"plan"`
	Skipped int             `json:"skipped"`
}
type StalePermitResult struct {
	Reconciled int `json:"reconciled"`
}
type stalePermitAudit struct {
	Entry         StalePermitPin `json:"entry"`
	Before        []byte         `json:"before"`
	Authorization string         `json:"authorization"`
	ReconciledAt  time.Time      `json:"reconciled_at"`
	AfterSHA256   string         `json:"after_sha256"`
}

func recordHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func staleAuditKey(p StalePermitPin) string {
	return "executor/stale-permit-audit/" + p.Pin.PermitID + "-" + p.RecordSHA256 + ".json"
}

func ValidStalePermitPlan(p StalePermitPlan) bool {
	if len(p.Permits) == 0 || len(p.Permits) > maxStalePermits {
		return false
	}
	previous := ""
	for _, entry := range p.Permits {
		if !validPin(entry.Pin) || entry.Pin.PermitID <= previous || !digestPattern.MatchString(entry.RecordSHA256) {
			return false
		}
		previous = entry.Pin.PermitID
	}
	return true
}

type stalePermitProof func(context.Context, Snapshot, time.Time) error

// PreviewStalePermitsAuthority opens existing authority read-only. It never
// creates a lock, persists an observation, closes admission or stops a unit.
func PreviewStalePermitsAuthority(ctx context.Context) (StalePermitPreview, error) {
	c, err := openAuthority(ctx, true)
	if err != nil {
		return StalePermitPreview{}, err
	}
	return c.previewStalePermits(ctx, time.Now().UTC(), proveStalePermit)
}
func (c *Controller) previewStalePermits(ctx context.Context, now time.Time, proof stalePermitProof) (StalePermitPreview, error) {
	out := StalePermitPreview{Plan: StalePermitPlan{Permits: []StalePermitPin{}}}
	if c.db == nil || proof == nil {
		return out, ErrHold
	}
	// One paged scan filters terminal history; only live candidates pay the
	// authoritative per-record read (two sqlite3 starts each for root).
	var pins []Pin
	seen := 0
	err := c.db.ScanContext(ctx, "executor/permits/", func(k string, raw []byte) error {
		if seen++; seen > maxHealthPermits {
			return ErrHold
		}
		var stored Snapshot
		if len(raw) > 8192 || json.Unmarshal(raw, &stored) != nil || k != key(stored.Pin) {
			return ErrHold
		}
		if stored.State != "FENCED" {
			pins = append(pins, stored.Pin)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	for _, pin := range pins {
		s, b, err := c.read(ctx, pin)
		if err != nil {
			return out, err
		}
		if s.State == "FENCED" {
			continue
		}
		if proof(ctx, s, now) != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Skipped++
			continue
		}
		if len(out.Plan.Permits) == maxStalePermits {
			return out, ErrHold
		}
		out.Plan.Permits = append(out.Plan.Permits, StalePermitPin{Pin: s.Pin, RecordSHA256: recordHash(b)})
	}
	sort.Slice(out.Plan.Permits, func(i, j int) bool { return out.Plan.Permits[i].Pin.PermitID < out.Plan.Permits[j].Pin.PermitID })
	return out, nil
}

// ReconcileStalePermitsAuthority holds all selected admission locks. Every
// record and OS proof is rechecked before the first write and again at its CAS.
// Each terminal transition and immutable audit receipt commit atomically.
func ReconcileStalePermitsAuthority(ctx context.Context, p StalePermitPlan, authorization string) (StalePermitResult, error) {
	if os.Geteuid() != 0 || !ValidStalePermitPlan(p) || authorization == "" {
		return StalePermitResult{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return StalePermitResult{}, err
	}
	var out StalePermitResult
	var locked func(int) error
	locked = func(index int) error {
		if index < len(p.Permits) {
			return c.WithPermitLock(ctx, p.Permits[index].Pin, func() error { return locked(index + 1) })
		}
		var e error
		out, e = c.reconcileStalePermits(ctx, p, authorization, time.Now().UTC(), proveStalePermit)
		return e
	}
	err = locked(0)
	return out, err
}
func (c *Controller) staleEntry(ctx context.Context, entry StalePermitPin, now time.Time, proof stalePermitProof) (Snapshot, []byte, bool, error) {
	s, raw, err := c.read(ctx, entry.Pin)
	if err != nil {
		return s, nil, false, err
	}
	if s.State == "FENCED" {
		audit, err := c.db.GetContext(ctx, staleAuditKey(entry))
		var a stalePermitAudit
		if err != nil || json.Unmarshal(audit, &a) != nil || a.Entry != entry || recordHash(a.Before) != entry.RecordSHA256 || a.AfterSHA256 != recordHash(raw) || a.Authorization == "" || a.ReconciledAt.IsZero() {
			return s, nil, false, ErrHold
		}
		return s, raw, true, nil
	}
	if len(raw) > 8192 || recordHash(raw) != entry.RecordSHA256 || proof(ctx, s, now) != nil {
		return s, nil, false, ErrHold
	}
	return s, raw, false, nil
}
func (c *Controller) reconcileStalePermits(ctx context.Context, p StalePermitPlan, authorization string, now time.Time, proof stalePermitProof) (StalePermitResult, error) {
	out := StalePermitResult{}
	if !ValidStalePermitPlan(p) || authorization == "" || proof == nil {
		return out, ErrHold
	}
	for _, entry := range p.Permits {
		if _, _, _, err := c.staleEntry(ctx, entry, now, proof); err != nil {
			return out, err
		}
	}
	for _, entry := range p.Permits {
		s, raw, done, err := c.staleEntry(ctx, entry, now, proof)
		if err != nil {
			return out, err
		}
		if done {
			out.Reconciled++
			continue
		}
		if s.Revision == ^uint64(0) {
			return out, ErrHold
		}
		s.State = "FENCED"
		s.Revision++
		for i := range s.Launches {
			if s.Launches[i].Identity == nil {
				s.Launches[i].State = "ABANDONED"
			} else {
				s.Launches[i].State = "FENCED"
			}
		}
		replacement, err := json.Marshal(s)
		if err != nil {
			return out, err
		}
		audit, err := json.Marshal(stalePermitAudit{Entry: entry, Before: raw, Authorization: authorization, ReconciledAt: now, AfterSHA256: recordHash(replacement)})
		if err != nil {
			return out, err
		}
		ok, err := c.db.ReplaceWithJournal(key(entry.Pin), raw, replacement, staleAuditKey(entry), audit)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, ErrHold
		}
		out.Reconciled++
	}
	return out, nil
}
