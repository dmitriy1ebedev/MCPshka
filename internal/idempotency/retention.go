package idempotency

import (
	"time"

	"mcpshka/internal/statestore"
)

// prunableOperations are retry receipts for ordinary, bounded actions. Pending
// (UNKNOWN) records and authority-bearing operations such as deploys, releases,
// integration, broker, admin or recovery receipts are never pruned.
var prunableOperations = []string{
	"exec_start", "fs_patch", "fs_write",
	"project_lease_acquire", "project_lease_resume", "project_lease_release", "project_lease_heartbeat",
	"project_run", "project_test", "project_build", "project-task",
	"knowledge_write", "knowledge-diag-compat", "job_cancel", "memory_write",
}

// Retention is how long a finished receipt keeps answering retries.
const Retention = 72 * time.Hour

func pruneRule(now time.Time, limit int) statestore.PruneRule {
	ops := make([]string, 0, 2*len(prunableOperations))
	for _, op := range prunableOperations {
		ops = append(ops, op, "contract/"+op)
	}
	return statestore.PruneRule{Operations: ops, States: []string{string(Completed), string(Failed)}, FinishedBefore: now.Add(-Retention), Limit: limit}
}

// Prune removes one bounded batch of expired finished receipts.
func Prune(db *statestore.DB, now time.Time, limit int) (int, error) {
	return db.PruneFinished("idempotency/", pruneRule(now, limit))
}

// StartPruner trims expired receipts in small batches so the state database
// stops growing with every retry key ever issued.
func StartPruner(db *statestore.DB, interval time.Duration, report func(int, error)) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				n, err := Prune(db, now, 500)
				if report != nil && (n > 0 || err != nil) {
					report(n, err)
				}
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}
