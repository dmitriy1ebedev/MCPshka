package projectops

import (
	"errors"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
)

func CleanupAdmission(m *jobs.Manager, leases *projectlease.Service, path string, action func() error) error {
	view, err := leases.Status(path)
	if err != nil {
		return err
	}
	return m.WithIdleProject(view.ProjectRoot, func() error {
		current, err := leases.Status(view.ProjectRoot)
		if err != nil {
			return err
		}
		if current.Held {
			return errors.New("candidate cleanup writer lease held: HOLD")
		}
		return action()
	})
}
