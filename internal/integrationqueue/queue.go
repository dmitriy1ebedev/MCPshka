// Package integrationqueue implements a deterministic, fail-closed integration
// queue for independently developed worktree branches.
package integrationqueue

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

type State string

const (
	Pending     State = "pending"
	Validated   State = "validated"
	Integrating State = "integrating"
	Integrated  State = "integrated"
	Failed      State = "failed"
	Cancelled   State = "cancelled"
)

type Item struct {
	ID     string
	Branch string
	Commit string
	State  State
}
type Queue struct {
	mu    sync.Mutex
	items []Item
}

const maxItems = 10000

var requestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var hexCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,180}$`)

func safeBranch(v string) bool {
	if !branchName.MatchString(v) || v == "HEAD" || v == "main" || v == "master" || v[len(v)-1] == '/' || v[len(v)-1] == '.' || strings.Contains(v, "..") || strings.Contains(v, "//") || strings.Contains(v, "@{") {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// ValidSourceBranch validates a disposable development branch, never main.
func ValidSourceBranch(v string) bool { return safeBranch(v) }

func (q *Queue) Enqueue(id, branch, commit string) error {
	if !requestID.MatchString(id) || !safeBranch(branch) || !hexCommit.MatchString(commit) {
		return errors.New("invalid integration request")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.ID == id {
			if item.Branch == branch && item.Commit == commit {
				return nil
			}
			return errors.New("idempotency conflict")
		}
	}
	if len(q.items) >= maxItems {
		return errors.New("queue capacity exceeded")
	}
	q.items = append(q.items, Item{ID: id, Branch: branch, Commit: commit, State: Pending})
	return nil
}
func (q *Queue) Validate(id, commit string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if q.items[i].ID == id {
			if q.items[i].State != Pending || q.items[i].Commit != commit {
				return errors.New("validation precondition failed")
			}
			q.items[i].State = Validated
			return nil
		}
	}
	return errors.New("unknown request")
}

// Claim serializes the integration mutation while allowing any number of
// independent workers to enqueue and validate candidate branches.
func (q *Queue) Claim() (Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.State == Integrating {
			return Item{}, errors.New("integrator busy")
		}
	}
	for i := range q.items {
		if q.items[i].State == Validated {
			q.items[i].State = Integrating
			return q.items[i], nil
		}
	}
	return Item{}, errors.New("no validated candidate")
}
func (q *Queue) Complete(id, commit string, success bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		item := &q.items[i]
		if item.ID == id {
			if item.State != Integrating || item.Commit != commit {
				return errors.New("completion precondition failed")
			}
			if success {
				item.State = Integrated
			} else {
				item.State = Failed
			}
			return nil
		}
	}
	return fmt.Errorf("unknown request")
}
func (q *Queue) Snapshot() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Item(nil), q.items...)
}

// safeTargetBranch accepts the reserved integration target while preserving
// all syntax restrictions applied to feature branch names.
func safeTargetBranch(v string) bool {
	if v == "main" || v == "master" {
		return true
	}
	return safeBranch(v)
}
