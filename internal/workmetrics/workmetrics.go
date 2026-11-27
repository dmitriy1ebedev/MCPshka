// Package workmetrics measures the irritating failures of ROADMAP P23: missions
// that started but were never accepted, leases nobody holds any more and
// operations stuck in UNKNOWN. Each value names its source; a scan that hit its
// budget is reported incomplete instead of guessed.
package workmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"mcpshka/internal/statestore"
	"mcpshka/internal/toolstats"
)

const (
	// StrandedAfter matches the stale-release rule: a lease whose heartbeat is
	// this old is held by nobody alive.
	StrandedAfter = 2 * time.Hour
	// StuckAfter: an idempotent call still pending this long lost its reply.
	StuckAfter = 15 * time.Minute
	scanBudget = 4000
)

var errBudget = errors.New("scan budget")

type Missions struct {
	Started  int     `json:"started"`
	Accepted int     `json:"accepted"` // DONE
	Running  int     `json:"running"`
	Stopped  int     `json:"stopped"` // BLOCKED, PAUSED, EXPIRED, CANCELLED
	Ratio    float64 `json:"accepted_per_started"`
}

type Stranded struct {
	Root  string `json:"project_root"`
	Owner string `json:"owner"`
	Age   string `json:"heartbeat_age"`
}

type Work struct {
	Outcomes        Outcomes            `json:"outcomes"`
	Discoverability toolstats.Discovery `json:"discoverability"`
	At              time.Time           `json:"at"`
	Missions        Missions            `json:"missions"`
	StrandedLeases  []Stranded          `json:"stranded_leases"`
	StuckUnknown    int                 `json:"stuck_unknown"`
	StuckOperations map[string]int      `json:"stuck_by_operation,omitempty"`
	Source          string              `json:"source"`
	Complete        bool                `json:"complete"`
	Note            string              `json:"note,omitempty"`
}

// Compute reads the live state store once per prefix within ctx.
func Compute(ctx context.Context, db *statestore.DB, now time.Time) Work {
	w := Work{Outcomes: outcomes(ctx, db, now), At: now, StrandedLeases: []Stranded{}, Source: "MCPshka state store (missions/, leases/, idempotency/) read now", Complete: true}
	if db == nil {
		w.Complete, w.Note = false, "state store is not SQLite on this host"
		return w
	}
	scan := func(prefix string, visit func([]byte)) {
		n := 0
		err := db.ScanContext(ctx, prefix, func(_ string, data []byte) error {
			if n++; n > scanBudget {
				return errBudget
			}
			visit(data)
			return nil
		})
		if err != nil {
			w.Complete = false
			w.Note += prefix + " " + err.Error() + "; "
		}
	}
	scan("missions/", func(data []byte) {
		var m struct {
			State string `json:"state"`
		}
		if json.Unmarshal(data, &m) != nil {
			return
		}
		w.Missions.Started++
		switch m.State {
		case "DONE":
			w.Missions.Accepted++
		case "READY", "RUNNING":
			w.Missions.Running++
		default:
			w.Missions.Stopped++
		}
	})
	if w.Missions.Started > 0 {
		w.Missions.Ratio = float64(w.Missions.Accepted) / float64(w.Missions.Started)
	}
	scan("leases/", func(data []byte) {
		var l struct {
			Root      string    `json:"project_root"`
			Owner     string    `json:"owner"`
			Acquired  time.Time `json:"acquired_at"`
			Heartbeat time.Time `json:"heartbeat_at"`
		}
		if json.Unmarshal(data, &l) != nil || l.Root == "" {
			return
		}
		seen := l.Heartbeat
		if seen.IsZero() {
			seen = l.Acquired
		}
		if age := now.Sub(seen); age >= StrandedAfter {
			w.StrandedLeases = append(w.StrandedLeases, Stranded{Root: l.Root, Owner: l.Owner, Age: age.Round(time.Minute).String()})
		}
	})
	scan("idempotency/", func(data []byte) {
		var e struct {
			Operation string    `json:"operation"`
			State     string    `json:"state"`
			Started   time.Time `json:"started_at"`
		}
		if json.Unmarshal(data, &e) != nil || e.State != "pending" || now.Sub(e.Started) < StuckAfter {
			return
		}
		w.StuckUnknown++
		if w.StuckOperations == nil {
			w.StuckOperations = map[string]int{}
		}
		w.StuckOperations[e.Operation]++
	})
	return w
}
