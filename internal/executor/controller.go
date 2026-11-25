// Package executor records launch admission before any process is started.
// The database is a trusted-controller store, never worker-writable authority.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/statestore"
	"mcpshka/internal/unitfence"
	"os"
	"regexp"
	"time"
)

var ErrHold = failure.ErrHold
var idPattern = regexp.MustCompile("^[0-9a-f]{32}$")

type Pin struct {
	PermitID    string `json:"permit_id"`
	LeaseID     string `json:"lease_id"`
	Generation  uint64 `json:"generation"`
	ExecutionID string `json:"execution_id"`
}
type Identity = unitfence.Identity
type Launch struct {
	ID          string    `json:"id"`
	State       string    `json:"state"`
	Identity    *Identity `json:"identity,omitempty"`
	FenceReason string    `json:"fence_reason,omitempty"`
	ReservedAt  time.Time `json:"reserved_at,omitempty"`
}
type Snapshot struct {
	Queue      *heavyqueue.Status `json:"queue,omitempty"`
	CreatedAt  time.Time          `json:"created_at,omitempty"`
	NextAction string             `json:"next_action,omitempty"`
	Pin        Pin                `json:"pin"`
	Revision   uint64             `json:"revision"`
	State      string             `json:"state"`
	Launches   []Launch           `json:"launches"`
	Result     *ExecutionResult   `json:"result,omitempty"`
	Source     *SourceSnapshot    `json:"source,omitempty"`
}
type Controller struct {
	db        *statestore.DB
	authority bool
}

func New(db *statestore.DB) *Controller { return &Controller{db: db} }
func validPin(p Pin) bool {
	return idPattern.MatchString(p.PermitID) && idPattern.MatchString(p.LeaseID) && p.Generation > 0 && idPattern.MatchString(p.ExecutionID)
}
func generationKey(p Pin) string {
	return fmt.Sprintf("executor/generations/%s-%d.json", p.LeaseID, p.Generation)
}
func key(p Pin) string { return "executor/permits/" + p.PermitID + ".json" }
func (c *Controller) Create(p Pin) error {
	if c.db == nil || !validPin(p) {
		return Hold(failure.PolicyDenied, "PERMIT_INPUT_INVALID", "controller.Create", "database or permit pin invalid", nil)
	}
	binding, _ := json.Marshal(p)
	ok, e := c.db.PutIfAbsent(generationKey(p), binding)
	if e != nil {
		return e
	}
	if !ok {
		return Hold(failure.OwnerBusy, "GENERATION_ALREADY_BOUND", "controller.Create", "lease generation already bound", nil)
	}
	b, _ := json.Marshal(Snapshot{CreatedAt: time.Now().UTC(), Pin: p, Revision: 1, State: "ACTIVE", Launches: []Launch{}})
	ok, e = c.db.PutIfAbsent(key(p), b)
	if e != nil {
		return e
	}
	if !ok {
		return Hold(failure.OwnerBusy, "PERMIT_ALREADY_EXISTS", "controller.Create", "permit already exists", nil)
	}
	return nil
}
func (c *Controller) read(ctx context.Context, p Pin) (Snapshot, []byte, error) {
	started := time.Now()
	if c.db == nil || !validPin(p) {
		return Snapshot{}, nil, Hold(failure.PolicyDenied, "PERMIT_INPUT_INVALID", "controller.read", "database or permit pin invalid", nil)
	}
	binding, e := c.db.GetContext(ctx, generationKey(p))
	if errors.Is(e, os.ErrNotExist) {
		return Snapshot{}, nil, Hold(failure.EvidenceMissing, "GENERATION_BINDING_MISSING", "controller.read", "generation binding missing", e)
	}
	if e != nil {
		return Snapshot{}, nil, contextFailure(ctx, "controller.read", started, e)
	}
	var bound Pin
	if json.Unmarshal(binding, &bound) != nil || bound != p {
		return Snapshot{}, nil, Hold(failure.FencedUnknown, "GENERATION_BINDING_MISMATCH", "controller.read", "generation bound to another pin", nil)
	}
	b, e := c.db.GetContext(ctx, key(p))
	if e != nil {
		return Snapshot{}, nil, contextFailure(ctx, "controller.read", started, e)
	}
	var s Snapshot
	if json.Unmarshal(b, &s) != nil || s.Pin != p || s.Revision == 0 || len(s.Launches) > 1 {
		return Snapshot{}, nil, Hold(failure.FencedUnknown, "PERMIT_RECORD_INVALID", "controller.read", "permit record or revision invalid", nil)
	}
	switch s.State {
	case "ACTIVE", "REVOKING", "FENCED":
	default:
		return Snapshot{}, nil, Hold(failure.FencedUnknown, "PERMIT_STATE_INVALID", "controller.read", "unsupported permit state", nil)
	}
	seen := map[string]bool{}
	for _, l := range s.Launches {
		if !idPattern.MatchString(l.ID) || seen[l.ID] {
			return Snapshot{}, nil, Hold(failure.FencedUnknown, "LAUNCH_ID_INVALID", "controller.read", "invalid or duplicate launch id", nil)
		}
		seen[l.ID] = true
		switch l.State {
		case "LAUNCH_INTENT":
			if l.Identity != nil {
				return Snapshot{}, nil, Hold(failure.FencedUnknown, "INTENT_HAS_IDENTITY", "controller.read", "unregistered launch has identity", nil)
			}
		case "ABANDONED":
			if s.State != "FENCED" || l.Identity != nil {
				return Snapshot{}, nil, Hold(failure.FencedUnknown, "ABANDONED_STATE_INVALID", "controller.read", "abandoned launch is not fenced", nil)
			}
		case "REGISTERED", "FENCED":
			if l.Identity == nil || !unitfence.ValidIdentity(*l.Identity) || !heavyqueue.ExecutorCgroupMatches(l.Identity.Cgroup, l.ID) {
				return Snapshot{}, nil, Hold(failure.FencedUnknown, "LAUNCH_IDENTITY_INVALID", "controller.read", "launch identity unverified", nil)
			}
		default:
			return Snapshot{}, nil, Hold(failure.FencedUnknown, "LAUNCH_STATE_INVALID", "controller.read", "unsupported launch state", nil)
		}
		if s.State == "FENCED" && l.State != "FENCED" && l.State != "ABANDONED" {
			return Snapshot{}, nil, Hold(failure.FencedUnknown, "FENCE_INCOMPLETE", "controller.read", "fenced permit has unresolved launch", nil)
		}
	}
	return s, b, nil
}
func (c *Controller) Inspect(ctx context.Context, p Pin) (Snapshot, error) {
	started := time.Now()
	s, _, e := c.read(ctx, p)
	if e == nil {
		e = c.attachResult(ctx, &s)
	}
	if e != nil {
		e = Hold(failure.FencedUnknown, "PERMIT_INSPECTION_UNVERIFIED", "controller.Inspect", "permit or result inspection unverified", contextFailure(ctx, "controller.Inspect", started, e))
	}
	return s, e
}
func (c *Controller) change(ctx context.Context, p Pin, f func(*Snapshot) error) error {
	started := time.Now()
	for range 16 {
		if e := ctx.Err(); e != nil {
			return contextFailure(ctx, "controller.change", started, e)
		}
		s, b, e := c.read(ctx, p)
		if e != nil {
			return e
		}
		if e = f(&s); e != nil {
			return e
		}
		if s.Revision == ^uint64(0) {
			return Hold(failure.FencedUnknown, "REVISION_EXHAUSTED", "controller.change", "permit revision overflow", nil)
		}
		s.Revision++
		n, e := json.Marshal(s)
		if e != nil {
			return e
		}
		ok, e := c.db.Replace(key(p), b, n)
		if e != nil {
			return e
		}
		if ok {
			return nil
		}
	}
	return Hold(failure.Transient, "TRANSITION_CONTENTION", "controller.change", "concurrent transition budget", nil)
}

// Reserve is persisted BEFORE requesting systemd start. An ambiguous start must
// not be retried: its unresolved intent remains visible across restart.
func (c *Controller) Reserve(ctx context.Context, p Pin, id string) error {
	started := time.Now()
	if c.db == nil || !validPin(p) || !idPattern.MatchString(id) {
		return Hold(failure.PolicyDenied, "LAUNCH_INPUT_INVALID", "controller.Reserve", "invalid launch admission input", nil)
	}
	if e := ctx.Err(); e != nil {
		return contextFailure(ctx, "controller.Reserve", started, e)
	}
	binding, _ := json.Marshal(p)
	ok, e := c.db.PutIfAbsent("executor/launches/"+id+".json", binding)
	if e != nil {
		return e
	}
	if !ok {
		return Hold(failure.OwnerBusy, "LAUNCH_ALREADY_BOUND", "controller.Reserve", "launch id already bound", nil)
	}
	return c.change(ctx, p, func(s *Snapshot) error {
		if s.State != "ACTIVE" || len(s.Launches) > 0 {
			return Hold(failure.FencedUnknown, "LAUNCH_NOT_ADMISSIBLE", "controller.Reserve", "launch requires ACTIVE permit without prior launch", nil)
		}
		for _, l := range s.Launches {
			if l.ID == id {
				return Hold(failure.FencedUnknown, "LAUNCH_ALREADY_PRESENT", "controller.Reserve", "launch id already present", nil)
			}
		}
		s.Launches = append(s.Launches, Launch{ID: id, State: "LAUNCH_INTENT", ReservedAt: time.Now().UTC()})
		return nil
	})
}
func (c *Controller) Close(ctx context.Context, p Pin) error {
	return c.change(ctx, p, func(s *Snapshot) error {
		if s.State == "FENCED" {
			return Hold(failure.FencedUnknown, "PERMIT_ALREADY_FENCED", "controller.Close", "permit already FENCED", nil)
		}
		s.State = "REVOKING"
		return nil
	})
}

// CompleteFence cannot turn unresolved or registered launches into proof.
func (c *Controller) CompleteFence(ctx context.Context, p Pin) error {
	return c.change(ctx, p, func(s *Snapshot) error {
		if s.State != "REVOKING" {
			return Hold(failure.FencedUnknown, "FENCE_REQUIRES_REVOKING", "controller.CompleteFence", "fence requires REVOKING permit", nil)
		}
		for _, l := range s.Launches {
			if l.State != "FENCED" {
				return Hold(failure.FencedUnknown, "LAUNCH_NOT_FENCED", "controller.CompleteFence", "fence: launch not fenced", nil)
			}
		}
		s.State = "FENCED"
		return nil
	})
}

// Register gates worker execution and accounts for late registration during revocation.
func (c *Controller) Register(ctx context.Context, p Pin, id string, i Identity) error {
	if !idPattern.MatchString(id) || !unitfence.ValidIdentity(i) || !heavyqueue.ExecutorCgroupMatches(i.Cgroup, id) {
		return Hold(failure.PolicyDenied, "REGISTRATION_IDENTITY_INVALID", "controller.Register", "registration: invalid launch identity", nil)
	}
	return c.change(ctx, p, func(s *Snapshot) error {
		if s.State == "FENCED" {
			return Hold(failure.FencedUnknown, "PERMIT_ALREADY_FENCED", "controller.Register", "permit already FENCED", nil)
		}
		for n, l := range s.Launches {
			if l.ID == id && l.State == "LAUNCH_INTENT" {
				s.Launches[n].State = "REGISTERED"
				copy := i
				s.Launches[n].Identity = &copy
				return nil
			}
		}
		return Hold(failure.FencedUnknown, "LAUNCH_INTENT_MISSING", "controller.Register", "launch not registered: matching launch intent absent", nil)
	})
}

// FenceBackend must match and stop the exact registered incarnation and verify
// an empty cgroup. An error, including absent/ambiguous identity, keeps HOLD.
type FenceBackend interface {
	StopAndVerify(context.Context, string, Identity) error
}

func (c *Controller) Fence(ctx context.Context, p Pin, b FenceBackend) error {
	started := time.Now()
	if b == nil {
		return Hold(failure.FencedUnknown, "FENCE_BACKEND_MISSING", "controller.Fence", "fence backend missing", nil)
	}
	s, e := c.Inspect(ctx, p)
	if e != nil {
		return e
	}
	if s.State == "FENCED" {
		return nil
	}
	if e := c.Close(ctx, p); e != nil {
		return e
	}
	s, e = c.Inspect(ctx, p)
	if e != nil {
		return e
	}
	for _, l := range s.Launches {
		if l.State == "FENCED" {
			continue
		}
		if l.State != "REGISTERED" || l.Identity == nil {
			return Hold(failure.FencedUnknown, "FENCE_IDENTITY_UNVERIFIED", "controller.Fence", "fence: launch not registered; identity unverified", nil)
		}
		fenceReason := "stopped by controller"
		if e = b.StopAndVerify(ctx, l.ID, *l.Identity); e != nil {
			// RuntimeMax may stop a worker after it persisted its result.
			// Closed admission plus the original incarnation/cgroup proof is sufficient.
			observer, ok := b.(stoppedObserver)
			if s.Result == nil || !ok || observer.VerifyStopped(ctx, l.ID, *l.Identity) != nil {
				return Hold(failure.FencedUnknown, "FENCE_VERIFICATION_FAILED", "controller.Fence", "fence: stop or identity verification failed", contextFailure(ctx, "controller.Fence", started, e))
			}
			fenceReason = "stopped by systemd"
		}
		id := l.ID
		identity := *l.Identity
		if e = c.change(ctx, p, func(s *Snapshot) error {
			if s.State != "REVOKING" {
				return Hold(failure.FencedUnknown, "FENCE_STATE_CHANGED", "controller.Fence", "permit no longer revoking", nil)
			}
			for n, x := range s.Launches {
				if x.ID == id && x.State == "REGISTERED" && x.Identity != nil && *x.Identity == identity {
					s.Launches[n].State = "FENCED"
					s.Launches[n].FenceReason = fenceReason
					return nil
				}
			}
			return Hold(failure.FencedUnknown, "FENCE_IDENTITY_CHANGED", "controller.Fence", "registered launch identity changed", nil)
		}); e != nil {
			return e
		}
	}
	if e = c.CompleteFence(ctx, p); e != nil {
		return e
	}
	releaseFencedUnits(ctx, b, s.Launches)
	return nil
}
