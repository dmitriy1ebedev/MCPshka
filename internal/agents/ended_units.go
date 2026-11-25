package agents

import (
	"context"
	"time"
)

// Both units must be observed inactive; a failed probe never proves death.
func TaskUnitsEnded(id string) bool {
	if !ValidTaskID(id) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, unit := range []string{"mcpshka-agent-task-runner-" + id + ".service", "mcpshka-agent-task-" + id + ".service"} {
		if ended, err := systemdUnits.ended(ctx, unit); err != nil || !ended {
			return false
		}
	}
	return true
}
