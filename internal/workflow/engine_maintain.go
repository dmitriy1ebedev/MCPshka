package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mcpshka/internal/jobs"
	"mcpshka/internal/roadmapscan"
)

const maintenanceCursorPrefix = "mcpshka-maintenance-v2:"

// Legacy cursors contain only the claim key. v2 persists a monotonically
// increasing generation so repeated passes over a single claim still have
// distinct CAS operands. Corrupt cursors are HOLD rather than reset-to-start.
func decodeMaintenanceCursor(raw []byte) (string, uint64, error) {
	if len(raw) == 0 {
		return "", 0, nil
	}
	stored := string(raw)
	var generation uint64
	if strings.HasPrefix(stored, maintenanceCursorPrefix) {
		payload := strings.TrimPrefix(stored, maintenanceCursorPrefix)
		if len(payload) <= 17 || payload[16] != ':' {
			return "", 0, errors.New("invalid workflow maintenance cursor: HOLD")
		}
		var err error
		generation, err = strconv.ParseUint(payload[:16], 16, 64)
		if err != nil || fmt.Sprintf("%016x", generation) != payload[:16] || generation == 0 {
			return "", 0, errors.New("invalid workflow maintenance generation: HOLD")
		}
		stored = payload[17:]
	}
	if !strings.HasPrefix(stored, "pipelines/v1/claims/") ||
		len(stored) > 256 || len(stored) == len("pipelines/v1/claims/") {
		return "", 0, errors.New("invalid workflow maintenance claim: HOLD")
	}
	for _, char := range stored[len("pipelines/v1/claims/"):] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char == '-' || char == '_') {
			return "", 0, errors.New("invalid workflow maintenance cursor suffix: HOLD")
		}
	}
	return stored, generation, nil
}

func nextMaintenanceCursor(previous []byte, key string) ([]byte, error) {
	_, generation, err := decodeMaintenanceCursor(previous)
	if err != nil {
		return nil, err
	}
	if generation == ^uint64(0) {
		return nil, errors.New("workflow maintenance cursor overflow: HOLD")
	}
	if _, _, err := decodeMaintenanceCursor([]byte(key)); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%s%016x:%s", maintenanceCursorPrefix, generation+1, key)), nil
}

// advanceMaintenanceCursor must acquire the exact SQLite cursor generation
// before allowing a stage to Tick. A second maintenance worker observing a
// stale cursor loses admission: the cursor is not merely advisory pagination.
func (e *Engine) advanceMaintenanceCursor(key string, previous, next []byte) error {
	var (
		advanced bool
		err      error
	)
	if previous == nil {
		advanced, err = e.db.PutIfAbsent(key, next)
	} else {
		advanced, err = e.db.Replace(key, previous, next)
	}
	if err != nil {
		return err
	}
	if !advanced {
		return errors.New("pipeline maintenance cursor changed: HOLD")
	}
	return nil
}

// Maintain advances at most one transition per active run and ten runs per pass.
// Durable admission prevents restarting an effect after a lost server response.
func (e *Engine) Maintain() error {
	if e.db == nil {
		return nil
	}
	completionErr := e.maintainCompletion()
	keys, err := e.db.List("pipelines/v1/claims/")
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return completionErr
	}
	cursorKey := "pipelines/v1/maintenance_cursor"
	previous, err := e.db.Get(cursorKey)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lastKey, _, err := decodeMaintenanceCursor(previous)
	if err != nil {
		return err
	}
	start := sort.SearchStrings(keys, lastKey)
	if start < len(keys) && keys[start] == lastKey {
		start++
	}
	firstError := completionErr
	for i := 0; i < min(len(keys), 10); i++ {
		key := keys[(start+i)%len(keys)]
		next, err := nextMaintenanceCursor(previous, key)
		if err != nil {
			return err
		}
		if err := e.advanceMaintenanceCursor(cursorKey, previous, next); err != nil {
			return err
		}
		previous = next
		id, err := e.db.Get(key)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if _, err := e.Tick(string(id)); err != nil {
			if firstError == nil {
				firstError = err
			}
		}
	}
	return firstError
}

// Resume only clears HOLD after the pinned plan and a real job receipt agree.
// It cannot turn a missing/UNKNOWN exit into success or replay a reserved job.
func (e *Engine) Resume(id string) (Run, error) {
	r, err := e.Get(id)
	if err != nil || r.State != "hold" {
		return r, err
	}
	if err := e.requireWorkflowClaim(r); err != nil {
		return r, err
	}
	_, plans, err := e.plans(r.Root, r.Pipeline)
	if err != nil {
		return r, err
	}
	raw, _ := json.Marshal(plans)
	if planDigest(raw, r.Schema) != r.Fingerprint {
		return r, errors.New("profile still differs")
	}
	if _, err := roadmapscan.Load(filepath.Join(r.Root, "ROADMAP.md"), r.RoadmapSHA); err != nil {
		return r, err
	}
	if r.LaunchClaimed {
		job, err := e.manager.Get(r.JobID)
		if err != nil {
			return r, err
		}
		if job.State == jobs.Unknown || job.State == jobs.Queued || job.State == jobs.Running || job.State == jobs.Cancelling {
			return r, errors.New("job outcome requires reconciliation")
		}
	}
	n := r
	n.State = "pending"
	n.Reason = ""
	if n.LaunchClaimed {
		n.State = "running"
	}
	n, err = e.save(r, n)
	if err != nil {
		return n, err
	}
	return e.Tick(id)
}
