package notify

import (
	"context"
	"encoding/json"
	"time"

	"mcpshka/internal/statestore"
)

// keep is how long a closed event, or an open INFO one, stays in the feed.
const keep = 72 * time.Hour

// prune frees the bounded namespace: closed events and INFO events older than
// keep go first, so a full feed never blocks a new warning.
func prune(db *statestore.DB, now time.Time) (int, error) {
	type old struct {
		key string
		raw []byte
	}
	var drop []old
	err := db.ScanContext(context.Background(), prefix, func(key string, raw []byte) error {
		var e Event
		if json.Unmarshal(raw, &e) != nil {
			drop = append(drop, old{key, raw})
			return nil
		}
		if (e.Closed || e.Severity == "INFO") && now.Sub(e.ObservedAt) > keep {
			drop = append(drop, old{key, raw})
		}
		return nil
	})
	n := 0
	for _, d := range drop {
		if ok, derr := db.Delete(d.key, d.raw); derr == nil && ok {
			n++
		}
	}
	return n, err
}
