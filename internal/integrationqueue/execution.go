package integrationqueue

import (
	"context"
	"errors"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"sync"
	"time"
)

// IntegrationAuthority is a trusted controller boundary, never a caller
// supplied approval flag. It must validate pinned review/test receipts and
// hold the repository writer fence synchronously across the entire effect.
type IntegrationAuthority interface {
	WithIntegrationFence(context.Context, RuntimeItem, func() error) error
}

func NewAuthorizedRuntime(db *statestore.DB, p *policy.Policy, authority IntegrationAuthority) *Runtime {
	s := NewRuntime(db, p)
	if authority != nil {
		s.withAuthority = authority.WithIntegrationFence
	}
	return s
}

// Execute persists the integration intent before Git and records completion
// only after observing the exact target SHA. UNKNOWN retains the durable
// reservation; cold recovery observes Git before considering another effect.
func (s *Runtime) Execute(ctx context.Context, path, id string) (RuntimeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s.withAuthority == nil || !requestID.MatchString(id) {
		return RuntimeStatus{}, errors.New("trusted integration authority unavailable: HOLD")
	}
	root, key, err := s.queue(ctx, path)
	if err != nil {
		return RuntimeStatus{}, err
	}
	record, _, err := s.read(ctx, key)
	if err != nil {
		return RuntimeStatus{}, err
	}
	initial := runtimeStatus(root, key, id, record)
	if !initial.Found || initial.Item.State == Cancelled {
		return RuntimeStatus{}, errors.New("integration not executable")
	}
	var mu sync.Mutex
	active, called := true, false
	var out RuntimeStatus
	var effectErr error
	fenceErr := s.withAuthority(ctx, *initial.Item, func() error {
		mu.Lock()
		defer mu.Unlock()
		if !active || called {
			return errors.New("integration fence callback reused or expired")
		}
		called = true
		out, effectErr = s.executeFenced(ctx, root, key, id, *initial.Item)
		return effectErr
	})
	mu.Lock()
	active = false
	completed := called
	mu.Unlock()
	if !completed {
		return RuntimeStatus{}, errors.New("integration fence did not execute")
	}
	if effectErr != nil {
		return out, effectErr
	}
	if fenceErr != nil {
		return out, errors.New("integration authority failed after effect: UNKNOWN")
	}
	return out, nil
}

func (s *Runtime) executeFenced(ctx context.Context, root, key, id string, pinned RuntimeItem) (RuntimeStatus, error) {
	// Linked worktrees share a queue, but a caller's clean checkout cannot
	// substitute for the source pinned in the approved candidate.
	source, sourceKey, err := s.queue(ctx, pinned.Request.Path)
	if err != nil || source != pinned.Request.Path || sourceKey != key {
		return RuntimeStatus{}, errors.New("pinned source authority changed: HOLD")
	}
	root = source
	for range 32 {
		record, raw, err := s.read(ctx, key)
		if err != nil {
			return RuntimeStatus{}, err
		}
		seen := runtimeStatus(root, key, id, record)
		if !seen.Found || seen.Item.Request != pinned.Request || seen.Item.ExecutionID != pinned.ExecutionID {
			return RuntimeStatus{}, errors.New("integration pins changed: HOLD")
		}
		if seen.Item.State == Integrated {
			return seen, nil
		}
		if seen.Item.State != Pending && seen.Item.State != Integrating {
			return RuntimeStatus{}, errors.New("integration no longer executable")
		}
		for _, other := range record.Items {
			if other.Request.ID != id && other.State == Integrating {
				return RuntimeStatus{}, errors.New("repository integration reservation busy: HOLD")
			}
		}
		if seen.Item.State == Pending {
			if err := s.verify(ctx, seen.Item.Request); err != nil {
				return RuntimeStatus{}, err
			}
			for i := range record.Items {
				if record.Items[i].Request.ID == id {
					record.Items[i].State = Integrating
				}
			}
			ok, err := s.write(key, raw, record)
			if err != nil {
				return RuntimeStatus{}, err
			}
			if !ok {
				continue
			}
			seen = runtimeStatus(root, key, id, record)
		}
		// Recovery probes before the injectable crash boundary and any mutation.
		head, err := directTargetHead(ctx, root, "refs/heads/"+pinned.Request.TargetBranch)
		if err != nil {
			return seen, errors.New("target outcome unverified: UNKNOWN")
		}
		if head != pinned.Request.Commit {
			if s.beforeGit != nil {
				if err := s.beforeGit(); err != nil {
					return seen, err
				}
			}
		}
		_, err = applyFastForward(ctx, root, pinned.Request.TargetBranch, pinned.Request.TargetHead, pinned.Request.Commit, pinned.ExecutionID, nil)
		if err != nil {
			return seen, err
		}
		// Re-read the CAS record after the effect: concurrent queue admissions
		// must survive, and a failed completion write must never replay Git.
		for range 32 {
			current, previous, err := s.read(ctx, key)
			if err != nil {
				return seen, err
			}
			found := false
			for i := range current.Items {
				item := &current.Items[i]
				if item.Request.ID != id {
					continue
				}
				if item.Request != pinned.Request || item.ExecutionID != pinned.ExecutionID || (item.State != Integrating && item.State != Integrated) {
					return seen, errors.New("post-effect reservation changed: UNKNOWN")
				}
				item.State = Integrated
				found = true
			}
			if !found {
				return seen, errors.New("post-effect reservation missing: UNKNOWN")
			}
			ok, err := s.write(key, previous, current)
			if err != nil {
				return seen, err
			}
			if ok {
				return runtimeStatus(root, key, id, current), nil
			}
		}
		return seen, errors.New("completion CAS contention: UNKNOWN")
	}
	return RuntimeStatus{}, errors.New("integration admission contention: HOLD")
}
