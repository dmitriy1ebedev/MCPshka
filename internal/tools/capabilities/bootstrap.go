package capabilities

import (
	"mcpshka/internal/failure"
	"mcpshka/internal/readiness"
)

// SetBootstrapReadiness expects capabilities in name order so the first
// blocker stays stable across calls.
func (out *CapabilityBootstrap) SetBootstrapReadiness(caps []readiness.CapabilityReadiness) {
	out.Ready = out.BackendHealthy == "true"
	if out.Ready {
		return
	}
	out.Blocker = "no fresh backend readiness evidence"
	out.NextSafeAction = &failure.Action{Tool: "health_snapshot", Arguments: map[string]any{}}
	for _, c := range caps {
		if out.BackendHealthy == "false" && c.State != readiness.Blocked && c.State != readiness.Degraded ||
			out.BackendHealthy == "unknown" && c.State != readiness.Preflight {
			continue
		}
		reason := c.Reason
		if reason == "" {
			reason = "backend readiness state is " + c.State
		}
		out.Blocker = c.Name + ": " + reason
		out.NextSafeAction = &failure.Action{Tool: "capability_selftest", Arguments: map[string]any{"names": []string{c.Name}}}
		return
	}
}
