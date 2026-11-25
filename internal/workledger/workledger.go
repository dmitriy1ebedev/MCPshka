// Package workledger is the durable source of truth of one unit of work:
// an atomic claim with an owner epoch (MCP-063) and an append-only ledger of
// confirmed effects written in the same SQLite transaction as the epoch check
// (MCP-062). A taken-over owner cannot record an effect, and a resumed run
// rebuilds its stage by replaying the ledger, never from an agent's memory.
package workledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"mcpshka/internal/statestore"
)

var (
	ErrClaimed = errors.New("WORK_CLAIMED_BY_ANOTHER_OWNER")
	ErrFenced  = errors.New("WORK_CLAIM_FENCED: epoch superseded; reconcile before any effect")
	validName  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,120}$`)
)

type Claim struct {
	Work     string `json:"work"`
	Owner    string `json:"owner"`
	Epoch    int    `json:"epoch"`
	Seq      int    `json:"seq"`
	Expires  int64  `json:"expires_unix"`
	Released bool   `json:"released,omitempty"`
}

type Event struct {
	Epoch int             `json:"epoch"`
	Seq   int             `json:"seq"`
	Owner string          `json:"owner"`
	Stage string          `json:"stage"`
	State string          `json:"state"` // CLAIMED effect: INTENT, CONFIRMED, UNKNOWN, FAILED
	At    time.Time       `json:"at"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type Ledger struct {
	DB  *statestore.DB
	Now func() time.Time
}

func claimKey(work string) string { return "workledger/" + work + "/claim" }
func eventKey(work string, e, s int) string {
	return fmt.Sprintf("workledger/%s/ev/%08d-%06d", work, e, s)
}

func (l Ledger) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Acquire wins the claim atomically: a free, released or expired claim moves
// to epoch+1; a live claim of another owner refuses; the same owner gets its
// own claim back (idempotent retry after a lost reply).
func (l Ledger) Acquire(work, owner string, ttl time.Duration) (Claim, error) {
	if !validName.MatchString(work) || owner == "" || ttl <= 0 {
		return Claim{}, errors.New("invalid work claim")
	}
	now := l.now()
	raw, err := l.DB.Get(claimKey(work))
	next := Claim{Work: work, Owner: owner, Epoch: 1, Expires: now.Add(ttl).Unix()}
	if errors.Is(err, os.ErrNotExist) {
		b, _ := json.Marshal(next)
		if ok, err := l.DB.PutIfAbsent(claimKey(work), b); err != nil || ok {
			return next, err
		}
		return Claim{}, ErrClaimed
	}
	if err != nil {
		return Claim{}, err
	}
	var cur Claim
	if err := json.Unmarshal(raw, &cur); err != nil {
		return Claim{}, err
	}
	if !cur.Released && cur.Expires > now.Unix() {
		if cur.Owner == owner {
			return cur, nil
		}
		return Claim{}, ErrClaimed
	}
	next.Epoch, next.Seq = cur.Epoch+1, cur.Seq
	b, _ := json.Marshal(next)
	if ok, err := l.DB.Replace(claimKey(work), raw, b); err != nil || !ok {
		if err == nil {
			err = ErrClaimed
		}
		return Claim{}, err
	}
	return next, nil
}

// Record appends one effect event fenced by the exact claim bytes: if another
// owner took the claim over, neither the claim nor the ledger changes.
func (l Ledger) Record(c Claim, stage, state string, data json.RawMessage) (Claim, error) {
	expected, _ := json.Marshal(c)
	next := c
	next.Seq++
	replacement, _ := json.Marshal(next)
	ev, _ := json.Marshal(Event{Epoch: c.Epoch, Seq: next.Seq, Owner: c.Owner, Stage: stage, State: state, At: l.now().UTC(), Data: data})
	ok, err := l.DB.ReplaceWithJournal(claimKey(c.Work), expected, replacement, eventKey(c.Work, c.Epoch, next.Seq), ev)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, ErrFenced
	}
	return next, nil
}

// Release frees the claim for the next owner (epoch increments on acquire).
func (l Ledger) Release(c Claim) error {
	expected, _ := json.Marshal(c)
	c.Released = true
	b, _ := json.Marshal(c)
	if ok, err := l.DB.Replace(claimKey(c.Work), expected, b); err != nil || !ok {
		if err == nil {
			err = ErrFenced
		}
		return err
	}
	return nil
}

// Replayed is the state rebuilt from confirmed events only.
type Replayed struct {
	Events        []Event  `json:"events"`
	LastConfirmed string   `json:"last_confirmed_stage,omitempty"`
	Unknown       []string `json:"unknown_stages,omitempty"` // effects to reconcile, never to repeat
}

func (l Ledger) Replay(work string) (Replayed, error) {
	var r Replayed
	err := l.DB.Scan("workledger/"+work+"/ev/", func(_ string, raw []byte) error {
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		r.Events = append(r.Events, e)
		return nil
	})
	open := map[string]bool{}
	for _, e := range r.Events {
		switch e.State {
		case "INTENT":
			open[e.Stage] = true
		case "CONFIRMED":
			delete(open, e.Stage)
			r.LastConfirmed = e.Stage
		case "FAILED":
			delete(open, e.Stage)
		case "UNKNOWN":
			open[e.Stage] = true
		}
	}
	for _, e := range r.Events {
		if open[e.Stage] {
			r.Unknown = append(r.Unknown, e.Stage)
			delete(open, e.Stage)
		}
	}
	return r, err
}
