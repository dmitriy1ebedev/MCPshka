package selfheal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type Target struct {
	Kind, ID, Scope, Fingerprint string
}
type TargetObservation struct {
	Owned, KnownFailure, NativeBusy, NativeExhausted bool
	// Adapter evidence must bind the original job/artifact/unit and scope.
	ObservedAt time.Time
}
type TargetBackend interface {
	Observe(context.Context, Target) (TargetObservation, error)
	Repair(context.Context, Target, string) (string, error)
	Verify(context.Context, Target, string) (bool, error)
	Rollback(context.Context, Target, string) (bool, error)
}

// RepairScoped is explicit and adapter-gated; Step never invents host authority.
func (s *Service) RepairScoped(ctx context.Context, target Target, backend TargetBackend) (string, error) {
	if !s.config.Enabled {
		return "DISABLED", nil
	}
	if backend == nil || target.ID == "" || target.Scope == "" || target.Fingerprint == "" || len(target.ID) > 256 || len(target.Scope) > 512 || len(target.Fingerprint) > 128 {
		return "HOLD", errors.New("REPAIR_INVALID_TARGET")
	}
	fp, err := hex.DecodeString(target.Fingerprint)
	if err != nil || len(fp) != 32 || target.Fingerprint != strings.ToLower(target.Fingerprint) {
		return "HOLD", errors.New("REPAIR_INVALID_FINGERPRINT")
	}
	switch target.Kind {
	case "stale_job", "own_artifact":
	case "failed_unit":
		if !strings.HasPrefix(target.ID, "mcpshka-") || !strings.HasSuffix(target.ID, ".service") {
			return "ESCALATED", nil
		}
	default:
		return "ESCALATED", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	observed, err := backend.Observe(ctx, target)
	now := s.now().UTC()
	if err != nil || !observed.Owned || observed.ObservedAt.IsZero() || observed.ObservedAt.After(now) || now.Sub(observed.ObservedAt) > time.Minute {
		return "HOLD", nil
	}
	if !observed.KnownFailure {
		return "ESCALATED", nil
	}
	if observed.NativeBusy || !observed.NativeExhausted {
		return "WAIT_NATIVE", nil
	}
	hash := sha256.Sum256([]byte(target.Kind + "\x00" + target.Scope + "\x00" + target.ID))
	name := "host:" + hex.EncodeToString(hash[:])
	st, raw, err := s.read(ctx)
	if err != nil {
		return "HOLD", err
	}
	c := st.Containers[name]
	if c.Phase == "UNKNOWN" {
		return "UNKNOWN", nil
	}
	if c.Fingerprint == target.Fingerprint && c.Phase != "" {
		freshFailure := (c.Phase == "recovered" || c.Phase == "rolled_back") && len(c.History) > 0 && observed.ObservedAt.After(c.History[len(c.History)-1].At)
		if !freshFailure {
			return c.Phase, nil
		}
	}
	count := 0
	for _, r := range c.History {
		if !r.At.Before(now.Add(-Window)) {
			count++
		}
	}
	if count >= s.config.MaxRepairs {
		c.Fingerprint, c.Phase = target.Fingerprint, "ESCALATED_RATE_LIMIT"
		if err := s.event(&st, name, c, c.Phase, now); err != nil {
			return "HOLD", err
		}
		st.Containers[name] = c
		if err := s.save(st, raw); err != nil {
			return "HOLD", err
		}
		return c.Phase, nil
	}
	if len(st.Containers) >= 128 && c.Fingerprint == "" {
		return "HOLD", errors.New("REPAIR_CAPACITY")
	}
	c.Fingerprint = target.Fingerprint
	c.Phase = "UNKNOWN"
	c.History = append(c.History, Repair{now, target.Fingerprint, "reserved"})
	if len(c.History) > 32 {
		c.History = c.History[len(c.History)-32:]
	}
	if err := s.event(&st, name, c, "scoped repair reserved", now); err != nil {
		return "HOLD", err
	}
	st.Containers[name] = c
	if err := s.save(st, raw); err != nil {
		return "HOLD", err
	}
	receipt, applyErr := backend.Repair(ctx, target, target.Fingerprint)
	phase := "UNKNOWN"
	if applyErr == nil && receipt != "" {
		verified, verifyErr := backend.Verify(ctx, target, receipt)
		if verifyErr == nil && verified {
			phase = "recovered"
		} else if verifyErr == nil {
			rolledBack, rollbackErr := backend.Rollback(ctx, target, receipt)
			if rollbackErr == nil && rolledBack {
				phase = "rolled_back"
			} else {
				phase = "UNKNOWN"
			}
		}
	}
	st, raw, err = s.read(ctx)
	if err != nil {
		return "UNKNOWN", err
	}
	c = st.Containers[name]
	if c.Fingerprint != target.Fingerprint {
		return "UNKNOWN", errors.New("REPAIR_CHANGED")
	}
	c.Phase = phase
	c.History[len(c.History)-1].Result = phase
	if err := s.event(&st, name, c, phase, now); err != nil {
		return "UNKNOWN", err
	}
	st.Containers[name] = c
	return phase, s.save(st, raw)
}
