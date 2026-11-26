// Package codexcoord keeps queued AI work durable without executing or
// transferring authority. A live Codex turn and the repository writer remain
// owned by their existing actors until independently fenced.
package codexcoord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"mcpshka/internal/statestore"
)

type State string

const (
	Queued   State = "queued"
	Reserved State = "dispatch_reserved"
	Hold     State = "hold"
)

type Model struct {
	ID      string   `json:"id"`
	Efforts []string `json:"efforts"`
}

type Task struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Worktree   string `json:"worktree"`
	RoadmapSHA string `json:"roadmap_sha256"`
	SessionID  string `json:"session_id"`
	Message    string `json:"message"`
	Model      string `json:"model"`
	Effort     string `json:"reasoning_effort"`
	DispatchID string `json:"dispatch_id,omitempty"`
	State      State  `json:"state"`
	Revision   uint64 `json:"revision"`
}

var (
	validID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	validHex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	validModel   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	validSession = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

type Queue struct {
	db *statestore.DB
}

func New(db *statestore.DB) (*Queue, error) {
	if db == nil {
		return nil, errors.New("durable state store required")
	}
	return &Queue{db: db}, nil
}

func catalogAccepts(catalog []Model, model, effort string) bool {
	if !validModel.MatchString(model) {
		return false
	}
	for _, m := range catalog {
		if m.ID == model {
			return effort == "" || slices.Contains(m.Efforts, effort)
		}
	}
	return false
}

// ValidateShape checks only syntax and pin completeness. It does not prove
// model availability, session ownership, worktree ownership or write authority.
func ValidateShape(t Task) error {
	if !validID.MatchString(t.ID) || t.Provider != "codex" || !validSession.MatchString(t.SessionID) {
		return errors.New("invalid task identity or provider")
	}
	if t.Worktree == "/" || !utf8.ValidString(t.Worktree) || strings.ContainsRune(t.Worktree, 0) || !filepath.IsAbs(t.Worktree) || filepath.Clean(t.Worktree) != t.Worktree || !validHex.MatchString(t.RoadmapSHA) {
		return errors.New("missing pinned worktree or roadmap")
	}
	if len(t.Message) == 0 || len(t.Message) > 4096 || !utf8.ValidString(t.Message) || strings.TrimSpace(t.Message) == "" || strings.ContainsRune(t.Message, 0) || !validModel.MatchString(t.Model) {
		return errors.New("invalid task message or model syntax")
	}
	if t.Effort != "" && !slices.Contains([]string{"minimal", "low", "medium", "high", "xhigh"}, t.Effort) {
		return errors.New("unsupported reasoning effort syntax")
	}
	if t.DispatchID != "" || t.Revision != 0 || (t.State != "" && t.State != Queued) {
		return errors.New("plan must not claim existing execution")
	}
	return nil
}

func validTask(t Task, catalog []Model) error {
	if err := ValidateShape(t); err != nil {
		return err
	}
	if !catalogAccepts(catalog, t.Model, t.Effort) {
		return errors.New("model/effort not present in supplied catalog")
	}
	return nil
}

func key(id string) string { return "codexcoord/tasks/" + id }

func (q *Queue) Get(id string) (Task, error) {
	if !validID.MatchString(id) {
		return Task{}, errors.New("invalid task id")
	}
	data, err := q.db.Get(key(id))
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil || t.ID != id || (t.State != Queued && t.State != Hold && t.State != Reserved) || t.Revision == 0 {
		return Task{}, errors.New("invalid durable task receipt")
	}
	if (t.State == Reserved && !validID.MatchString(t.DispatchID)) || (t.State == Queued && t.DispatchID != "") {
		return Task{}, errors.New("invalid dispatch reservation")
	}
	return t, nil
}

// Enqueue is idempotent by immutable task identity and settings. It cannot
// start Codex or obtain a writer lease.
func (q *Queue) Enqueue(t Task, catalog []Model) (Task, error) {
	if t.DispatchID != "" || t.Revision != 0 || (t.State != "" && t.State != Queued) {
		return Task{}, errors.New("new task must not carry a previous execution receipt")
	}
	if err := validTask(t, catalog); err != nil {
		return Task{}, err
	}
	t.State, t.Revision = Queued, 1
	payload, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	ok, err := q.db.PutIfAbsent(key(t.ID), payload)
	if err != nil {
		return Task{}, err
	}
	if ok {
		return t, nil
	}
	existing, err := q.Get(t.ID)
	if err != nil {
		return Task{}, err
	}
	if existing != t {
		return Task{}, errors.New("durable task identity conflict")
	}
	return existing, nil
}

// Pending returns a bounded, stable view of work awaiting orchestration.
// It performs no Codex RPC and does not imply admission to a writer scope.
func (q *Queue) Pending(limit int) ([]Task, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("pending result budget must be 1..100")
	}
	ids, err := q.db.List("codexcoord/tasks/")
	if err != nil {
		return nil, err
	}
	if len(ids) > 128 {
		return nil, errors.New("pending scan budget exceeded: HOLD")
	}
	ready := make([]Task, 0)
	for _, k := range ids {
		t, err := q.Get(k[len("codexcoord/tasks/"):])
		if err != nil {
			return nil, err
		}
		if t.State == Queued {
			ready = append(ready, t)
			if len(ready) >= limit {
				break
			}
		}
	}
	return ready, nil
}

// Reconfigure changes only a queued task, never an active Codex turn.
// A CAS revision fences concurrent planners. No model switch is dispatched.
func (q *Queue) Reconfigure(id string, expected uint64, model, effort string, catalog []Model) (Task, error) {
	t, err := q.Get(id)
	if err != nil {
		return Task{}, err
	}
	if expected == ^uint64(0) || t.Revision != expected || t.State != Queued || !catalogAccepts(catalog, model, effort) {
		return Task{}, errors.New("queued task reconfiguration precondition failed")
	}
	prev, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	t.Model, t.Effort, t.Revision = model, effort, expected+1
	next, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	ok, err := q.db.Replace(key(id), prev, next)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, errors.New("task was modified concurrently: HOLD")
	}
	return t, nil
}

// Suspend records HOLD without deleting context or assuming that any worker
// stopped. Releasing a lease and resuming execution are separate operations.
func (q *Queue) Suspend(id string, expected uint64) (Task, error) {
	t, err := q.Get(id)
	if err != nil {
		return Task{}, err
	}
	if expected == ^uint64(0) || t.Revision != expected || t.State != Queued {
		return Task{}, errors.New("HOLD transition precondition failed")
	}
	prev, _ := json.Marshal(t)
	t.State, t.Revision = Hold, expected+1
	next, _ := json.Marshal(t)
	ok, err := q.db.Replace(key(id), prev, next)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, errors.New("concurrent HOLD transition")
	}
	return t, nil
}

// bindClaim is a durable no-expiry reservation, not an authorization.
// A crash between the two independent claims can leave a conservative HOLD.
// Neither age nor session loss permits deleting a claim.
func (q *Queue) bindClaim(path, owner string) error {
	_, err := q.db.PutIfAbsent(path, []byte(owner))
	if err != nil {
		return err
	}
	actual, err := q.db.Get(path)
	if err != nil || string(actual) != owner {
		return errors.New("foreign or unknown dispatch claim: HOLD")
	}
	return nil
}

func worktreeClaim(worktree string) string {
	digest := sha256.Sum256([]byte(worktree))
	return "codexcoord/claims/worktree/" + hex.EncodeToString(digest[:])
}

func (q *Queue) checkClaims(t Task) error {
	owner := t.ID + "\n" + t.DispatchID
	for _, path := range []string{"codexcoord/claims/session/" + t.SessionID, worktreeClaim(t.Worktree)} {
		actual, err := q.db.Get(path)
		if err != nil || string(actual) != owner {
			return errors.New("dispatch ownership not proven: HOLD")
		}
	}
	return nil
}

// Reserve persists both identity claims and a task CAS BEFORE any external
// Codex call. It deliberately never sends an RPC, creates a writer, or changes
// an active turn. Replaying the same dispatch ID can only read its receipt.
// Competing sessions/worktrees remain HOLD until a separately trusted fenced
// cleanup and release protocol is implemented.
func (q *Queue) Reserve(id string, expected uint64, dispatchID string) (Task, error) {
	if !validID.MatchString(dispatchID) {
		return Task{}, errors.New("invalid dispatch identity")
	}
	t, err := q.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.State == Reserved && t.DispatchID == dispatchID {
		if err := q.checkClaims(t); err != nil {
			return Task{}, err
		}
		return t, nil
	}
	if expected == ^uint64(0) || t.State != Queued || t.Revision != expected {
		return Task{}, errors.New("task is not eligible for dispatch reservation: HOLD")
	}
	owner := t.ID + "\n" + dispatchID
	for _, path := range []string{"codexcoord/claims/session/" + t.SessionID, worktreeClaim(t.Worktree)} {
		if err := q.bindClaim(path, owner); err != nil {
			return Task{}, err
		}
	}
	previous, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	t.State, t.Revision, t.DispatchID = Reserved, expected+1, dispatchID
	next, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	ok, err := q.db.Replace(key(id), previous, next)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, errors.New("dispatch reservation CAS lost: HOLD")
	}
	return t, nil
}

// InspectReservation never returns permission to dispatch. Even a complete
// reservation still requires a separate verified live-session, lease and
// trusted approval gate before a real worker call can be implemented.
type Reservation struct {
	Task            Task   `json:"task"`
	ClaimsVerified  bool   `json:"claims_verified"`
	DispatchEnabled bool   `json:"dispatch_enabled"`
	NextSafeAction  string `json:"next_safe_action"`
}

func (q *Queue) InspectReservation(id string) (Reservation, error) {
	t, err := q.Get(id)
	if err != nil {
		return Reservation{}, err
	}
	r := Reservation{Task: t, DispatchEnabled: false, NextSafeAction: "read_only_reconcile"}
	if t.State == Reserved {
		if err := q.checkClaims(t); err != nil {
			return r, err
		}
		r.ClaimsVerified = true
		r.NextSafeAction = "owner_authorization_and_protocol_acceptance_required"
	} else {
		// Claims commit before the task CAS; queued/HOLD does not prove no dispatch attempt.
		for _, path := range []string{"codexcoord/claims/session/" + t.SessionID, worktreeClaim(t.Worktree)} {
			_, err := q.db.Get(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return r, err
			}
			r.NextSafeAction = "reconcile_dispatch_claims_without_replay"
		}
	}
	return r, nil
}

// No AssertWritable/Dispatch is exposed: even verified claims do not confer
// session ownership, Git writer leases, privileged broker or policy authority.
