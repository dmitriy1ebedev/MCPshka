package orchestration

import (
	"errors"
	"strings"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/failure"
)

// mutationLockBusy is the root broker refusing before any effect because
// another configuration mutation holds its lock (two agent_task calls at
// once, 26.11): the prepared worktree and prompt must go, or they stay as
// orphans with a fenced key.
func mutationLockBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "configuration mutation busy")
}

// Only typed pre-dispatch refusals authorize rollback; lost replies stay fenced.
func dispatchPrepared(run func() (map[string]any, error), rollback func() error) (map[string]any, error) {
	out, err := run()
	if !errors.Is(err, brokerclient.ErrCircuitOpen) && !errors.Is(err, brokerclient.ErrBusy) && !mutationLockBusy(err) {
		return out, err
	}
	if cleanup := rollback(); cleanup != nil {
		return nil, errors.Join(err, cleanup)
	}
	return nil, failure.Rejected("AGENT_NOT_DISPATCHED", "agent_task", "preparation rolled back; retry the same key when the broker is ready", err)
}
