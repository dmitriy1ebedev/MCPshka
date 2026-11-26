// Package cloudtasks persists UNTRUSTED cloud worker observations in the existing
// SQLite WAL. It never dispatches workers, grants leases, signs artifacts or deploys.
package cloudtasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"

	"mcpshka/internal/statestore"
)

const prefix = "cloudtasks/tasks/"
const requestPrefix = "cloudtasks/requests/"
const eventPrefix = "cloudtasks/events/"
const maxRecords = 64
const maxEventReceipts = 1024

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var validRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
var validDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Plan struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	Repository   string `json:"repository"`
	SourceDigest string `json:"source_digest"`
	RequestID    string `json:"request_id"`
}
type Observation struct {
	EventID          string `json:"event_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	ProviderTaskID   string `json:"provider_task_id"`
	ProviderState    string `json:"provider_state"`
	SourceDigest     string `json:"source_digest"`
}
type Receipt struct {
	Plan
	ProviderTaskID   string `json:"provider_task_id,omitempty"`
	State            string `json:"state"`
	ProviderState    string `json:"provider_state,omitempty"`
	LastEventID      string `json:"last_event_id,omitempty"`
	LastEventSHA256  string `json:"last_event_sha256,omitempty"`
	Revision         uint64 `json:"revision"`
	Trust            string `json:"trust"`
	DispatchEnabled  bool   `json:"dispatch_enabled"`
	DeployAuthorized bool   `json:"deploy_authorized"`
}

type Journal struct{ db *statestore.DB }

func New(db *statestore.DB) (*Journal, error) {
	if db == nil {
		return nil, errors.New("SQLite state authority unavailable: HOLD")
	}
	return &Journal{db: db}, nil
}
func validPlan(p Plan) bool {
	return validID.MatchString(p.ID) && (p.Provider == "codex_cloud" || p.Provider == "copilot_cloud") &&
		validRepo.MatchString(p.Repository) && !strings.Contains(p.Repository, "..") &&
		validDigest.MatchString(p.SourceDigest) && validID.MatchString(p.RequestID)
}
func key(id string) string { return prefix + id }
func hashIdentity(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (j *Journal) Get(id string) (Receipt, error) {
	if j == nil || j.db == nil || !validID.MatchString(id) {
		return Receipt{}, errors.New("invalid task or database")
	}
	raw, err := j.db.Get(key(id))
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	if err = json.Unmarshal(raw, &out); err != nil || out.ID != id || !validPlan(out.Plan) || out.Revision < 1 ||
		out.Trust != "UNTRUSTED" || out.DispatchEnabled || out.DeployAuthorized {
		return Receipt{}, errors.New("invalid durable cloud task receipt: HOLD")
	}
	if out.State != "planned" && out.State != "hold" && out.State != "observed_completed" && out.State != "observed_failed" {
		return Receipt{}, errors.New("invalid durable cloud task state: HOLD")
	}
	if out.Revision == 1 && (out.State != "planned" || out.ProviderTaskID != "" || out.LastEventID != "" || out.LastEventSHA256 != "") {
		return Receipt{}, errors.New("invalid initial cloud task receipt: HOLD")
	}
	if out.Revision > 1 && (out.State == "planned" || !validID.MatchString(out.ProviderTaskID) || !validID.MatchString(out.LastEventID) ||
		!validDigest.MatchString(out.LastEventSHA256)) {
		return Receipt{}, errors.New("invalid observed cloud task receipt: HOLD")
	}
	claim, claimErr := j.db.Get(requestPrefix + out.RequestID)
	if claimErr != nil || string(claim) != hashIdentity(out.Plan) {
		return Receipt{}, errors.New("cloud request ownership not verified: HOLD")
	}
	if out.Revision > 1 {
		claimedEvent, eventErr := j.db.Get(eventPrefix + out.ID + "/" + out.LastEventID)
		if eventErr != nil || string(claimedEvent) != out.LastEventSHA256 {
			return Receipt{}, errors.New("cloud event fingerprint unavailable: HOLD")
		}
	}
	return out, nil
}

// Stage is an idempotent, non-executable planning receipt. Provider task identity
// is not claimed before independent authenticated dispatch.
func (j *Journal) Stage(p Plan) (Receipt, error) {
	if j == nil || j.db == nil || !validPlan(p) {
		return Receipt{}, errors.New("invalid cloud task plan")
	}
	// First reconcile a previously staged task. A conflicting replay must not
	// allocate a new request claim or consume the bounded journal capacity.
	prior, existingErr := j.Get(p.ID)
	if existingErr == nil {
		if prior.Plan != p {
			return Receipt{}, errors.New("cloud task ID conflicts with existing plan: HOLD")
		}
		return prior, nil
	}
	if !errors.Is(existingErr, os.ErrNotExist) {
		return Receipt{}, existingErr
	}
	// Reserve the request identity before writing a task. Conflicting plans or
	// interrupted writes fail closed; no worker has been dispatched.
	claimKey := requestPrefix + p.RequestID
	claimValue := []byte(hashIdentity(p))
	createdClaim, claimErr := j.db.PutIfAbsentBounded(claimKey, claimValue, requestPrefix, maxRecords)
	if claimErr != nil {
		return Receipt{}, claimErr
	}
	if !createdClaim {
		existing, err := j.db.Get(claimKey)
		if err != nil || string(existing) != string(claimValue) {
			return Receipt{}, errors.New("cloud request identity is bound to another task: HOLD")
		}
	}
	rec := Receipt{Plan: p, State: "planned", Revision: 1, Trust: "UNTRUSTED"}
	raw, err := json.Marshal(rec)
	if err != nil {
		return Receipt{}, err
	}
	created, err := j.db.PutIfAbsentBounded(key(p.ID), raw, prefix, maxRecords)
	if err != nil {
		return Receipt{}, err
	}
	if created {
		return rec, nil
	}
	prior, err = j.Get(p.ID)
	if errors.Is(err, os.ErrNotExist) {
		return Receipt{}, errors.New("cloud task journal capacity: HOLD")
	}
	if err != nil {
		return Receipt{}, err
	}
	if prior.Plan != p {
		return Receipt{}, errors.New("cloud task ID conflicts with existing plan: HOLD")
	}
	return prior, nil
}

// Observe never verifies the provider; caller-provided status is strictly
// untrusted progress. Exact event replay is idempotent; a changed payload,
// historical event-ID reuse or stale revision is a hard HOLD.
func (j *Journal) Observe(id string, o Observation) (Receipt, error) {
	if !validID.MatchString(id) || !validID.MatchString(o.EventID) || !validID.MatchString(o.ProviderTaskID) ||
		!validDigest.MatchString(o.SourceDigest) || len(o.ProviderState) > 50 {
		return Receipt{}, errors.New("invalid cloud task observation")
	}
	var state string
	rawStatus := strings.ToLower(o.ProviderState)
	switch rawStatus {
	case "completed":
		state = "observed_completed"
	case "failed", "cancelled", "timed_out":
		state = "observed_failed"
	case "queued", "running", "in_progress", "idle", "pending", "waiting_for_user", "unknown":
		state = "hold"
	default:
		return Receipt{}, errors.New("unrecognized cloud provider status: HOLD")
	}
	old, err := j.Get(id)
	if err != nil {
		return Receipt{}, err
	}
	if old.SourceDigest != o.SourceDigest {
		return Receipt{}, errors.New("cloud source digest mismatch: HOLD")
	}
	digest := hashIdentity(o)
	if old.LastEventID == o.EventID {
		if old.LastEventSHA256 != digest {
			return Receipt{}, errors.New("cloud event identity conflict: HOLD")
		}
		return old, nil
	}
	if old.Revision != o.ExpectedRevision || old.Revision == ^uint64(0) {
		return Receipt{}, errors.New("cloud task revision stale: HOLD")
	}
	if old.ProviderTaskID != "" && old.ProviderTaskID != o.ProviderTaskID {
		return Receipt{}, errors.New("cloud provider task rebound: HOLD")
	}
	// A terminal cloud task remains terminal: a new attempt must have a new
	// pinned plan and provider task identity, not rewrite previous evidence.
	if old.State == "observed_completed" || old.State == "observed_failed" {
		return Receipt{}, errors.New("cloud task already terminal: HOLD")
	}
	// Immutable bounded event fingerprints prevent reuse of a historical event
	// ID after later observations. If a crash occurs after claiming but before
	// CAS, an identical retry at the same revision may safely finish locally.
	eventKey := eventPrefix + id + "/" + o.EventID
	createdEvent, claimErr := j.db.PutIfAbsentBounded(eventKey, []byte(digest), eventPrefix, maxEventReceipts)
	if claimErr != nil {
		return Receipt{}, claimErr
	}
	if !createdEvent {
		previousDigest, claimErr := j.db.Get(eventKey)
		if claimErr != nil || string(previousDigest) != digest {
			return Receipt{}, errors.New("cloud event ID already bound to other evidence: HOLD")
		}
	}
	previous, _ := json.Marshal(old)
	next := old
	next.ProviderTaskID = o.ProviderTaskID
	next.ProviderState = rawStatus
	next.State = state
	next.LastEventID = o.EventID
	next.LastEventSHA256 = digest
	next.Revision++
	updated, _ := json.Marshal(next)
	won, err := j.db.Replace(key(id), previous, updated)
	if err != nil {
		return Receipt{}, err
	}
	if !won {
		// A concurrent identical event may have committed; reconcile from durable
		// state, never repeat unknown external side effects.
		current, e := j.Get(id)
		if e == nil && current.LastEventID == o.EventID && current.LastEventSHA256 == digest {
			return current, nil
		}
		return Receipt{}, errors.New("cloud observation CAS conflict: HOLD")
	}
	return next, nil
}
func (j *Journal) List(limit int) ([]Receipt, error) {
	if j == nil || j.db == nil || limit < 1 || limit > 20 {
		return nil, errors.New("cloud task list budget 1..20")
	}
	ids, err := j.db.List(prefix)
	if err != nil {
		return nil, err
	}
	if len(ids) > maxRecords {
		return nil, errors.New("cloud task journal scan exceeded budget: HOLD")
	}
	out := make([]Receipt, 0, limit)
	for _, path := range ids {
		rec, e := j.Get(strings.TrimPrefix(path, prefix))
		if e != nil {
			return nil, e
		}
		out = append(out, rec)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
