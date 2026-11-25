package mission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"mcpshka/internal/budget"
	"mcpshka/internal/evidence"
)

// maintainBudget is one background reconcile pass at idle load (scaled by
// budget.Phase); reconcileEvery spaces re-checks of a BLOCKED mission.
const maintainBudget = 45 * time.Second

var reconcileEvery = 2 * time.Minute

var releaseGates = []string{"engineering", "integration", "release", "deploy", "acceptance", "cleanup"}

func releaseGatesProven(gates []evidence.GateState) bool {
	for _, required := range releaseGates {
		found := false
		for _, gate := range gates {
			if gate.Gate == required && gate.State == "PASS" {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Maintain only reconciles evidence of existing tasks. It never dispatches AI or effects.
func (c *Controller) Maintain() error {
	if c.db == nil {
		return nil
	}
	// A full preflight walks every step (capability_preflight +
	// acceptance_status each): 5 s never fit a 20-step plan, so every pass
	// timed out and the timeouts themselves degraded mission readiness (08.11).
	ctx, cancel := context.WithTimeout(context.Background(), budget.Phase(maintainBudget))
	defer cancel()
	const cursorKey = "mission-maintenance/v1/cursor"
	previous, err := c.db.Get(cursorKey)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	keys, err := c.db.ListPage(ctx, "missions/", string(previous), 1)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		keys, err = c.db.ListPage(ctx, "missions/", "", 1)
		if err != nil || len(keys) == 0 {
			return err
		}
	}
	key := keys[0]
	var won bool
	if previous == nil {
		won, err = c.db.PutIfAbsent(cursorKey, []byte(key))
	} else {
		won, err = c.db.Replace(cursorKey, previous, []byte(key))
	}
	if err != nil || !won {
		return err
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, "missions/"), ".json")
	s, old, err := c.load(id)
	if err != nil {
		return err
	}
	switch s.State {
	case "DONE":
		return nil
	case "PAUSED", "CANCELLED", "EXPIRED":
		if s.WorkflowID == "" {
			return nil
		}
	}
	if s.WorkflowID != "" && c.WorkflowControl != nil {
		if err := c.controlWorkflow(ctx, s, "bind"); err != nil {
			return err
		}
	}
	if last, ok := c.lastReconcile.Load(id); ok && s.State == "BLOCKED" && time.Since(last.(time.Time)) < reconcileEvery {
		return nil
	}
	c.lastReconcile.Store(id, time.Now())
	before, _ := json.Marshal(s)
	if err := c.refresh(ctx, &s, true); err != nil {
		return err
	}
	after, _ := json.Marshal(s)
	if string(before) == string(after) {
		return nil
	}
	_, err = c.save(s, old, "reconcile", "existing task receipts and acceptance")
	return err
}
