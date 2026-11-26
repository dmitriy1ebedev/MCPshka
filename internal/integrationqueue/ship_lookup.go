package integrationqueue

import (
	"context"
	"errors"
)

func DefaultShipID(commit string) string {
	if !hexCommit.MatchString(commit) {
		return ""
	}
	return "ship-" + commit[:12]
}

// FindShip validates an explicit continuation and suggests an existing item
// for the commit if that ID is stale or points to a different candidate.
func (s *Runtime) FindShip(ctx context.Context, path, id, commit string) (Request, string, error) {
	if !hexCommit.MatchString(commit) {
		return Request{}, "", errors.New("ship lookup requires a full commit SHA")
	}
	_, key, err := s.queue(ctx, path)
	if err != nil {
		return Request{}, "", err
	}
	record, _, err := s.read(ctx, key)
	if err != nil {
		return Request{}, "", err
	}
	var pending, active string
	for _, item := range record.Items {
		req := item.Request
		if req.Commit != commit || req.TargetBranch != "main" || item.State == Cancelled {
			continue
		}
		if req.ID == id {
			return req, "", nil
		}
		if item.State == Integrated || item.State == Integrating {
			active = req.ID
		} else {
			pending = req.ID
		}
	}
	if active != "" {
		return Request{}, active, nil
	}
	return Request{}, pending, nil
}
