// Package notify stores a bounded owner event feed in the authoritative state DB.
package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"mcpshka/internal/statestore"
)

const (
	prefix   = "notify/events/"
	capacity = 256
)

type Event struct {
	Ref              *Reference `json:"ref,omitempty"`
	SourceObservedAt *time.Time `json:"source_observed_at,omitempty"`
	NextSafeAction   string     `json:"next_safe_action,omitempty"`
	Progress         *Progress  `json:"progress,omitempty"`
	Delivery         *Delivery  `json:"delivery,omitempty"`
	Key              string     `json:"key"`
	Severity         string     `json:"severity"`
	Message          string     `json:"message"`
	Source           string     `json:"source"`
	Count            int        `json:"count"`
	FirstAt          time.Time  `json:"first_at"`
	ObservedAt       time.Time  `json:"observed_at"`
	Closed           bool       `json:"closed"`
	Quiet            bool       `json:"quiet"`
}
type QuietHours struct {
	StartHour        int `json:"start_hour"`
	EndHour          int `json:"end_hour"`
	UTCOffsetMinutes int `json:"utc_offset_minutes"`
}

func (q QuietHours) Active(now time.Time) bool {
	if q.StartHour < 0 || q.StartHour > 23 || q.EndHour < 0 || q.EndHour > 23 || q.UTCOffsetMinutes < -720 || q.UTCOffsetMinutes > 840 {
		return false
	}
	h := now.UTC().Add(time.Duration(q.UTCOffsetMinutes) * time.Minute).Hour()
	if q.StartHour > q.EndHour {
		return h >= q.StartHour || h < q.EndHour
	}
	return h >= q.StartHour && h < q.EndHour
}

// Record uses compare-and-swap: duplicate observations only update count/time.
func Record(db *statestore.DB, e Event, recovered bool, now time.Time) error {
	if db == nil {
		return errors.New("notification state unavailable")
	}
	if e.Key == "" || len(e.Key) > 256 || len(e.Message) > 600 || len(e.Source) > 256 || len(e.NextSafeAction) > 512 || !validReference(e.Ref) {
		return errors.New("invalid event")
	}
	switch e.Severity {
	case "INFO", "WARNING", "BLOCKED", "FAILED":
	default:
		return errors.New("invalid severity")
	}
	sum := sha256.Sum256([]byte(e.Key))
	key := prefix + hex.EncodeToString(sum[:]) + ".json"
	var quiet QuietHours
	q, err := db.Get("notify/quiet-hours.json")
	if errors.Is(err, os.ErrNotExist) {
		q, err = nil, nil
	}
	if err != nil {
		return err
	}
	if len(q) > 0 && json.Unmarshal(q, &quiet) != nil {
		return errors.New("invalid quiet hours")
	}
	pruned := false
	for range 4 {
		raw, err := db.Get(key)
		if errors.Is(err, os.ErrNotExist) {
			raw, err = nil, nil
		}
		if err != nil {
			return err
		}
		var old Event
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
		}
		if old.Closed && e.Source == "face_id" {
			return nil
		}
		if recovered && (len(raw) == 0 || old.Closed) {
			return nil
		}
		if len(raw) > 0 && !old.Closed {
			if e.SourceObservedAt != nil && (old.SourceObservedAt == nil || !e.SourceObservedAt.Before(*old.SourceObservedAt)) {
				old.Ref = e.Ref
				old.SourceObservedAt = e.SourceObservedAt
				old.Progress = e.Progress
				old.NextSafeAction = e.NextSafeAction
			}
			old.Count++
			old.ObservedAt = now.UTC()
			if recovered {
				old.Closed = true
				if old.Delivery != nil {
					old.Delivery.State = "cancelled"
				}
			}
			e = old
		} else {
			e.Count = 1
			e.FirstAt = now.UTC()
			e.ObservedAt = now.UTC()
			e.Closed = false
			e.Quiet = quiet.Active(now)
			e.Delivery = &Delivery{State: "pending"}
		}
		if e.Delivery == nil {
			e.Delivery = &Delivery{State: "pending"}
			if e.Closed {
				e.Delivery.State = "cancelled"
			}
		}
		next, err := json.Marshal(e)
		if err != nil {
			return err
		}
		var ok bool
		if len(raw) == 0 {
			ok, err = db.PutIfAbsentBounded(key, next, prefix, capacity)
			if err == nil && !ok && !pruned {
				// A full feed: drop old closed/INFO events, then retry.
				pruned = true
				_, _ = prune(db, now)
			}
		} else {
			ok, err = db.Replace(key, raw, next)
		}
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("event changed concurrently")
}
func Open(ctx context.Context, db *statestore.DB) ([]Event, error) {
	out := []Event{}
	if db == nil {
		return out, errors.New("notification state unavailable")
	}
	err := db.ScanContext(ctx, prefix, func(_ string, raw []byte) error {
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if !e.Closed {
			out = append(out, e)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].Key < out[j].Key
		}
		return out[i].ObservedAt.After(out[j].ObservedAt)
	})
	return out, err
}
