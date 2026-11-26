package selfheal

import (
	"context"
	"time"

	"mcpshka/internal/dockerops"
)

const mdnsFailure = "go2rtc LAN mDNS socket missing"

// Unknown observations neither clear a failure nor prove recovery.
func (s *Service) missingMDNS(ctx context.Context, name string, v dockerops.Status) (bool, bool) {
	if name != "go2rtc" || !v.Running || v.Restarting {
		return false, true
	}
	probe, ok := s.docker.(interface {
		LANMDNS(context.Context) (dockerops.LANMDNS, error)
	})
	if !ok {
		return false, false
	}
	observation, err := probe.LANMDNS(ctx)
	if err != nil || observation.StartedAt != v.StartedAt || observation.PID != v.PID {
		return false, false
	}
	if observation.LANIP == "" {
		return false, false
	}
	return !observation.Bound, true
}

func mdnsRestartDue(v dockerops.Status, c Container, now time.Time) bool {
	started, err := time.Parse(time.RFC3339Nano, v.StartedAt)
	if err != nil || now.Sub(started) < 2*time.Minute {
		return false
	}
	for _, r := range c.History {
		if now.Sub(r.At) < 15*time.Minute {
			return false
		}
	}
	return true
}
