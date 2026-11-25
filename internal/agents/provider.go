package agents

import (
	"mcpshka/internal/providers"
	"time"
)

// WithProvider reports fresh provider evidence without treating quota as failure.
func WithProvider(t Task, now time.Time) Task {
	t.Quota = providers.ObservedQuota(nil, nil, time.Time{}, "", now)
	if t.State == "quota" {
		zero := float64(0)
		var reset *time.Time
		if !t.ResetAt.IsZero() {
			reset = &t.ResetAt
		}
		t.Quota = providers.ObservedQuota(&zero, reset, t.FinishedAt, "agent_task:"+t.ID, now)
	}
	unknown := providers.Signal{State: "unknown", Reason: "нет подтверждённых данных"}
	t.Identity = providers.Identity{ID: t.Agent, Kind: "reasoner", Trust: "UNTRUSTED", Capabilities: []string{"ask", "review", "execute"}, Authenticated: unknown, Health: unknown, Quota: t.Quota}
	return t
}
