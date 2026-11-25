package integrationqueue

import (
	"errors"
	"fmt"
)

// RecoveryEvidence is independently observed Git state. It must be collected
// from the target repository after process restart, not from queue metadata.
type RecoveryEvidence struct {
	TargetHead         string
	CandidateReachable bool
	CandidateMerged    bool
	CleanTarget        bool
}

// ReconcileFailed resolves an uncertain integration only with explicit,
// externally verified Git evidence. Unknown outcomes stay failed.
func (q *Queue) ReconcileFailed(id, commit string, evidence RecoveryEvidence) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !hexCommit.MatchString(evidence.TargetHead) || !evidence.CleanTarget {
		return errors.New("missing clean target Git evidence")
	}
	for i := range q.items {
		item := &q.items[i]
		if item.ID != id {
			continue
		}
		if item.State != Failed || item.Commit != commit {
			return errors.New("recovery precondition failed")
		}
		if !evidence.CandidateReachable {
			return errors.New("candidate commit unavailable")
		}
		if evidence.CandidateMerged {
			item.State = Integrated
			return nil
		}
		// A previously failed integration must never be automatically retried:
		// an operator or an explicit orchestrator policy may enqueue a new request.
		return fmt.Errorf("candidate not merged; new validated request required")
	}
	return errors.New("unknown request")
}
