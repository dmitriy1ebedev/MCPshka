package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mcpshka/internal/statestore"
	"regexp"
	"time"
)

type Reference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Progress struct {
	CheckpointRef    string    `json:"checkpoint_ref"`
	PlannedStep      string    `json:"planned_step"`
	ObservedState    string    `json:"observed_state"`
	HoldReason       string    `json:"hold_reason"`
	SafeParallelWork string    `json:"safe_parallel_work"`
	Freshness        time.Time `json:"freshness"`
}
type Delivery struct {
	State    string    `json:"state"`
	Attempts int       `json:"attempts"`
	NextAt   time.Time `json:"next_at"`
}

var referenceID = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:/-]{0,191}$")

func validReference(r *Reference) bool {
	if r == nil {
		return true
	}
	if !referenceID.MatchString(r.ID) {
		return false
	}
	switch r.Kind {
	case "job", "issue", "release", "challenge", "checkpoint":
		return true
	}
	return false
}
func eventKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return prefix + hex.EncodeToString(h[:]) + ".json"
}

// Sender must return delivered, not_delivered (confirmed no effect), or UNKNOWN.
// The stable event key is the transport deduplication identity.
type Sender interface {
	Send(context.Context, Event, string) (string, error)
}

func Deliver(ctx context.Context, db *statestore.DB, key string, sender Sender, now time.Time) error {
	if db == nil || sender == nil {
		return errors.New("NOTIFY_DELIVERY_UNAVAILABLE")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path := eventKey(key)
	raw, err := db.Get(path)
	if err != nil {
		return err
	}
	var e Event
	if json.Unmarshal(raw, &e) != nil {
		return errors.New("NOTIFY_CORRUPTED")
	}
	if e.Closed || e.Quiet {
		return nil
	}
	if e.Delivery == nil {
		e.Delivery = &Delivery{State: "pending"}
	}
	d := e.Delivery
	if d.State == "delivered" || d.State == "sent" || d.State == "cancelled" || d.State == "UNKNOWN" || d.State == "sending" || d.Attempts >= 3 || now.Before(d.NextAt) {
		return nil
	}
	d.State = "sending"
	d.Attempts++
	reserved, _ := json.Marshal(e)
	ok, err := db.Replace(path, raw, reserved)
	if err != nil || !ok {
		return errors.New("NOTIFY_CHANGED")
	}
	state, sendErr := sender.Send(ctx, e, e.Key)
	if sendErr != nil || state != "delivered" && state != "not_delivered" {
		state = "UNKNOWN"
	}
	d.State = state
	if state == "delivered" {
		d.State = "sent"
	}
	if state == "not_delivered" {
		d.State = "failed"
		d.NextAt = now.Add(time.Duration(1<<uint(d.Attempts)) * time.Minute)
	}
	next, _ := json.Marshal(e)
	// Confirmation/closure wins over an in-flight delivery result.
	_, err = db.Replace(path, reserved, next)
	return err
}

type Challenge struct {
	ID         string
	Pending    bool
	ObservedAt time.Time
}

func ObserveChallenges(db *statestore.DB, items []Challenge, now time.Time) error {
	if len(items) > 64 {
		return errors.New("NOTIFY_CHALLENGE_BUDGET")
	}
	for _, c := range items {
		if !validReference(&Reference{"challenge", c.ID}) {
			return errors.New("NOTIFY_INVALID_CHALLENGE")
		}
		observed := c.ObservedAt
		e := Event{Key: "face-id/" + c.ID, Severity: "BLOCKED", Source: "face_id", Ref: &Reference{"challenge", c.ID}, SourceObservedAt: &observed, NextSafeAction: "owner_portal", Message: "Ожидается Face ID"}
		if err := Record(db, e, !c.Pending, now); err != nil {
			return err
		}
	}
	return nil
}
