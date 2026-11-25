package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync/atomic"
	"time"

	"mcpshka/internal/authn"
)

type shipRunKey struct{}
type shipRun struct {
	done   chan struct{}
	step   atomic.Value
	result IntegrationShipResult
	err    error
}

// The chat waits at most 15 seconds across the whole conveyor, including host
// reads and nested tools. Durable child retry keys remain authoritative on restart.
func (r *toolRegistry) ship(ctx context.Context, env shipEnv, in IntegrationShipInput) (IntegrationShipResult, error) {
	identity, _ := authn.IdentityFromContext(ctx)
	raw, _ := json.Marshal(struct {
		Input    IntegrationShipInput
		Identity authn.Identity
	}{in, identity})
	key := sha256.Sum256(raw)
	fresh := &shipRun{done: make(chan struct{})}
	value, loaded := r.shipRuns.LoadOrStore(key, fresh)
	run := value.(*shipRun)
	if !loaded {
		go func() {
			child, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			run.result, run.err = r.shipStep(context.WithValue(child, shipRunKey{}, run), env, in)
			close(run.done)
			// A client may never return for its reply; bound retained completed results.
			time.AfterFunc(10*time.Minute, func() { r.shipRuns.CompareAndDelete(key, run) })
		}()
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-run.done:
		r.shipRuns.CompareAndDelete(key, run)
		return run.result, run.err
	case <-timer.C:
	case <-ctx.Done():
	}
	item := in.ID
	if item == "" && len(in.Commit) >= 12 {
		item = "ship-" + in.Commit[:12]
	}
	step, _ := run.step.Load().(string)
	return IntegrationShipResult{Stage: "SHIP_RUNNING", Commit: in.Commit, ItemID: item, Detail: step + " is running in the background", NextAction: "call integration_ship again with the same arguments to observe progress"}, nil
}
