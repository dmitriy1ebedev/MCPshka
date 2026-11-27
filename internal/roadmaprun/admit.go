package roadmaprun

import (
	"context"
	"time"

	"mcpshka/internal/agents"
)

// HealthAdmission admits an agent unless fresh local evidence proves it
// cannot work now (MCP-100). Missing evidence admits: the attempt itself is
// the probe, and quota exhaustion is tracked by the runner from the reply.
func HealthAdmission(ctx context.Context, agent string) (bool, string) {
	for _, p := range agents.Health(ctx, time.Now().UTC()) {
		if p.Name == agent && p.Authorization.State == "degraded" {
			return false, "its last task failed authentication (log in again)"
		}
	}
	return true, ""
}
