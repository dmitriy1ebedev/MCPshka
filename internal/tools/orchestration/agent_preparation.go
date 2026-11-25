package orchestration

import (
	"errors"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/failure"
)

// Only typed pre-dispatch refusals authorize rollback; lost replies stay fenced.
func dispatchPrepared(run func() (map[string]any, error), rollback func() error) (map[string]any, error) {
	out, err := run()
	if !errors.Is(err, brokerclient.ErrCircuitOpen) && !errors.Is(err, brokerclient.ErrBusy) {
		return out, err
	}
	if cleanup := rollback(); cleanup != nil {
		return nil, errors.Join(err, cleanup)
	}
	return nil, failure.Rejected("AGENT_NOT_DISPATCHED", "agent_task", "preparation rolled back; retry the same key when the broker is ready", err)
}
