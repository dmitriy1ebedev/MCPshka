package laneprovider

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Health struct {
	Authority string    `json:"authority"`
	Mode      string    `json:"mode"`
	UpdatedAt time.Time `json:"updated_at"`
	LastFull  time.Time `json:"last_full"`
	PathCount int       `json:"path_count"`
}

// Health reads healthd's existing snapshot via laned READ API, never confirm/full.
func (s *Service) Health(ctx context.Context) (Health, error) {
	if s == nil || s.call == nil {
		return Health{}, errors.New("LANE provider unavailable")
	}
	raw, e := s.call(ctx, "health", map[string]any{})
	if e != nil {
		return Health{}, e
	}
	if len(raw) > maxResponseBytes {
		return Health{}, errors.New("LANE health response exceeds limit")
	}
	var wire struct {
		OK    bool `json:"ok"`
		State struct {
			Version   int                        `json:"version"`
			Mode      string                     `json:"mode"`
			UpdatedAt time.Time                  `json:"updated_at"`
			LastFull  time.Time                  `json:"last_full"`
			Paths     map[string]json.RawMessage `json:"paths"`
		} `json:"state"`
	}
	if e = json.Unmarshal(raw, &wire); e != nil {
		return Health{}, e
	}
	if !wire.OK || wire.State.Version != 1 || len(wire.State.Mode) > 32 || len(wire.State.Paths) > 64 {
		return Health{}, errors.New("unverified LANE health snapshot")
	}
	return Health{Authority: "lane.health", Mode: wire.State.Mode, UpdatedAt: wire.State.UpdatedAt, LastFull: wire.State.LastFull, PathCount: len(wire.State.Paths)}, nil
}
