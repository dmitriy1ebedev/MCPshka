package jobs

import (
	"errors"
	"mcpshka/internal/policy"
)

// launchRejected is issued only before a backend may have started any effect.
type launchRejected struct{ cause error }

func (e *launchRejected) Error() string { return e.cause.Error() }
func (e *launchRejected) Unwrap() error { return e.cause }
func rejectBeforeLaunch(err error) error {
	if err == nil || IsLaunchRejected(err) {
		return err
	}
	return &launchRejected{cause: err}
}
func IsLaunchRejected(err error) bool {
	var rejected *launchRejected
	return errors.As(err, &rejected)
}

func (m *Manager) commandDecision(command string, args []string, root string, isolated bool) (policy.CommandDecision, error) {
	if isolated {
		return m.policy.DecideIsolatedProjectTest(command, args, root)
	}
	return m.policy.DecideCommandForWorkdir(command, args, root)
}

// CheckCommand uses exactly the same runtime-aware policy choice as StartWithOptions.
func (m *Manager) CheckCommand(command string, args []string, root string) error {
	cwd, err := m.policy.CheckWorkdir(root)
	if err != nil {
		return err
	}
	m.mu.RLock()
	runtime := m.external
	m.mu.RUnlock()
	isolated := runtime != nil && runtime.Match(command, args, cwd)
	_, err = m.commandDecision(command, args, cwd, isolated)
	return err
}
