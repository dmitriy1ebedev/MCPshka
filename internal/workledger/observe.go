package workledger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mcpshka/internal/statestore"
)

// UnresolvedAt counts open intent or unknown stages as of an exclusive cutoff.
// Each durable work and stage is counted once, after replaying its events.
func UnresolvedAt(ctx context.Context, db *statestore.DB, since, until time.Time, age time.Duration, budget int) (int, error) {
	type stage struct{ work, name string }
	open := map[stage]time.Time{}
	seen := 0
	err := db.ScanContext(ctx, "workledger/", func(key string, raw []byte) error {
		seen++
		if seen > budget {
			return errors.New("work ledger scan budget exceeded")
		}
		parts := strings.Split(key, "/")
		if len(parts) != 4 || parts[2] != "ev" {
			return nil
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if e.At.IsZero() || !e.At.Before(until) || e.Stage == "" {
			return nil
		}
		k := stage{parts[1], e.Stage}
		switch e.State {
		case "PENDING", "UNKNOWN", "INTENT":
			if _, ok := open[k]; !ok {
				open[k] = e.At
			}
		case "CONFIRMED", "FAILED":
			delete(open, k)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, at := range open {
		if !at.Before(since) && until.Sub(at) >= age {
			n++
		}
	}
	return n, nil
}
