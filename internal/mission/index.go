package mission

import (
	"errors"
	"strings"
)

// Reference is a recorded hint, not fresh acceptance evidence.
type Reference struct {
	ID            string `json:"id"`
	RecordedState string `json:"recorded_state"`
}

func (c *Controller) Index() ([]Reference, bool, error) {
	out := []Reference{}
	if c.db == nil {
		return out, false, errors.New("mission requires SQLite")
	}
	keys, err := c.db.List("missions/")
	if err != nil {
		return out, false, err
	}
	truncated := len(keys) > 256
	if truncated {
		keys = keys[:256]
	}
	for _, key := range keys {
		id := strings.TrimSuffix(strings.TrimPrefix(key, "missions/"), ".json")
		s, _, err := c.load(id)
		if err != nil {
			return out, true, err
		}
		out = append(out, Reference{ID: s.ID, RecordedState: s.State})
	}
	return out, truncated, nil
}
