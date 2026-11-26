// Package runoutcome derives automatic run status from observed work.
package runoutcome

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"mcpshka/internal/notify"
	"mcpshka/internal/statestore"
)

type Result struct {
	State          string `json:"state"`
	Processed      int    `json:"processed"`
	Effect         bool   `json:"effect"`
	Blocker        string `json:"blocker,omitempty"`
	NextSafeAction string `json:"next_safe_action,omitempty"`
}

func Resolve(processed int, hasInput bool, blocker, next string) Result {
	r := Result{Processed: processed, Effect: processed > 0, Blocker: blocker, NextSafeAction: next}
	switch {
	case processed > 0 && blocker != "":
		r.State = "partial"
	case processed > 0:
		r.State = "completed"
	case hasInput || blocker != "":
		r.State = "no_effect"
		if r.Blocker == "" {
			r.Blocker = "no committed work was reported"
		}
		if r.NextSafeAction == "" {
			r.NextSafeAction = "inspect the source cursor and run receipt before retrying"
		}
	default:
		r.State = "idle"
	}
	return r
}

type observation struct {
	Run    string `json:"run"`
	Streak int    `json:"streak"`
	Result Result `json:"result"`
}

// Record stores the last outcome and raises an owner event on consecutive
// no-effect runs. Re-observing a receipt does not advance the streak.
func Record(db *statestore.DB, source, run string, r Result, now time.Time) error {
	if db == nil {
		return errors.New("automatic run state unavailable")
	}
	key := "runoutcome/" + source
	for range 4 {
		raw, err := db.Get(key)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var prev observation
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &prev); err != nil {
				return err
			}
		}
		if prev.Run == run {
			return nil
		}
		next := observation{Run: run, Result: r}
		if r.State == "no_effect" {
			next.Streak = prev.Streak + 1
		}
		event := notify.Event{Key: key, Source: "automatic_run", Severity: "WARNING", Message: source + ": " + r.Blocker, NextSafeAction: r.NextSafeAction}
		event.Message = event.Message[:min(600, len(event.Message))]
		event.NextSafeAction = event.NextSafeAction[:min(512, len(event.NextSafeAction))]
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		var ok bool
		if len(raw) == 0 {
			ok, err = db.PutIfAbsent(key, data)
		} else {
			ok, err = db.Replace(key, raw, data)
		}
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if next.Streak >= 2 || prev.Streak >= 2 && next.Streak == 0 {
			return notify.Record(db, event, next.Streak == 0, now)
		}
		return nil
	}
	return errors.New("automatic run outcome changed concurrently")
}
