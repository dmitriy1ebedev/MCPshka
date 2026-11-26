// Package durabletimer keeps periodic control events outside any agent turn
// (MCP-008). Timers live in the state store; a tick fires each due timer at
// most once per slot (dedup by an atomic slot record, safe across processes),
// coalesces slots missed during an outage into ONE event instead of a burst,
// and writes a heartbeat that a watchdog compares with the period.
package durabletimer

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"time"

	"mcpshka/internal/runoutcome"
	"mcpshka/internal/statestore"
)

type Timer struct {
	ID     string        `json:"id"`
	Start  time.Time     `json:"start"`
	Period time.Duration `json:"period"`
}

type Fire struct {
	Timer  string    `json:"timer"`
	Slot   int64     `json:"slot"`
	Missed int64     `json:"missed_coalesced"` // earlier slots skipped, not replayed
	At     time.Time `json:"at"`
}

type Store struct{ DB *statestore.DB }

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}$`)

func (s Store) Add(t Timer) error {
	if !validID.MatchString(t.ID) || t.Period < time.Minute {
		return errors.New("invalid timer (id, period >= 1m)")
	}
	b, _ := json.Marshal(t)
	if _, err := s.DB.PutIfAbsent("timers/"+t.ID+"/def", b); err != nil {
		return err
	}
	return nil
}

func (s Store) last(id string) (int64, error) {
	raw, err := s.DB.Get("timers/" + id + "/last")
	if errors.Is(err, os.ErrNotExist) {
		return -1, nil
	}
	if err != nil {
		return -1, err
	}
	return strconv.ParseInt(string(raw), 10, 64)
}

// Tick fires due timers. Concurrent ticks (two processes, a restarted
// service) cannot fire the same slot twice.
func (s Store) Tick(now time.Time) ([]Fire, error) {
	fires, _, err := s.TickWithOutcome(now)
	return fires, err
}

// TickWithOutcome reports published timer slots. The consumer reports its own work.
func (s Store) TickWithOutcome(now time.Time) ([]Fire, runoutcome.Result, error) {
	fires, contended, err := s.tick(now)
	blocker, next := "", ""
	if err != nil {
		blocker, next = "timer_store_unavailable", "inspect durable timer storage before retrying"
	} else if len(fires) == 0 && contended {
		blocker, next = "timer_slot_already_claimed", "observe the existing timer event before rescheduling"
	}
	out := runoutcome.Resolve(len(fires), contended, blocker, next)
	return fires, out, errors.Join(err, runoutcome.Record(s.DB, "durabletimer", now.UTC().Format(time.RFC3339Nano), out, now))
}

func (s Store) tick(now time.Time) ([]Fire, bool, error) {
	var fires []Fire
	var contended bool
	var timers []Timer
	err := s.DB.Scan("timers/", func(key string, raw []byte) error {
		var t Timer
		if len(key) <= 4 || key[len(key)-4:] != "/def" {
			return nil
		}
		if json.Unmarshal(raw, &t) != nil || !validID.MatchString(t.ID) || t.Period < time.Minute {
			return errors.New("invalid persisted timer definition")
		}
		timers = append(timers, t)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	for _, t := range timers {
		if now.Before(t.Start) {
			continue
		}
		slot := int64(now.Sub(t.Start) / t.Period)
		prev, err := s.last(t.ID)
		if err != nil {
			return fires, contended, err
		}
		if slot <= prev {
			continue
		}
		won, err := s.DB.PutIfAbsent("timers/"+t.ID+"/fired/"+strconv.FormatInt(slot, 10), []byte(now.UTC().Format(time.RFC3339)))
		if err != nil {
			return fires, contended, err
		}
		if !won {
			contended = true
			continue
		}
		missed := slot - prev - 1
		if prev < 0 {
			missed = 0
		}
		fires = append(fires, Fire{Timer: t.ID, Slot: slot, Missed: missed, At: now.UTC()})
		if err := s.DB.Put("timers/"+t.ID+"/last", []byte(strconv.FormatInt(slot, 10))); err != nil {
			return fires, contended, err
		}
	}
	return fires, contended, s.DB.Put("timers/_heartbeat", []byte(now.UTC().Format(time.RFC3339Nano)))
}

// Watchdog reports STALE when no tick happened for `grace`.
func (s Store) Watchdog(now time.Time, grace time.Duration) (string, time.Time) {
	raw, err := s.DB.Get("timers/_heartbeat")
	if errors.Is(err, os.ErrNotExist) {
		return "NEVER_TICKED", time.Time{}
	}
	at, perr := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil || perr != nil {
		return "UNKNOWN", time.Time{}
	}
	if now.Sub(at) > grace {
		return "STALE", at
	}
	return "OK", at
}
