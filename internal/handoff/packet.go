// Package handoff stores non-executable task packets and explicit export policy.
package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mcpshka/internal/statestore"
	"os"
	"regexp"
	"strings"
	"time"
)

type Field struct {
	Value    string `json:"value"`
	MinTrust string `json:"min_trust"`
	Secret   bool   `json:"secret"`
}
type Packet struct {
	Objective       string           `json:"objective"`
	Scope           string           `json:"scope"`
	HEAD            string           `json:"head"`
	Owner           string           `json:"owner"`
	LeaseID         string           `json:"lease_id"`
	RunID           string           `json:"run_id"`
	Checkpoint      string           `json:"checkpoint"`
	Receipts        []string         `json:"receipts"`
	Blockers        []string         `json:"blockers"`
	NextSafeAction  string           `json:"next_safe_action"`
	PlanFingerprint string           `json:"plan_fingerprint"`
	CreationKey     string           `json:"creation_key"`
	LoadKey         string           `json:"load_key"`
	ThreadID        string           `json:"thread_id"`
	SessionTreeID   string           `json:"session_tree_id"`
	JobID           string           `json:"job_id"`
	Generation      uint64           `json:"generation"`
	Stage           string           `json:"stage"`
	Provider        string           `json:"provider"`
	Fields          map[string]Field `json:"fields"`
}
type Policy struct {
	Provider string `json:"provider"`
	Tier     string `json:"tier"`
}
type authObservation struct {
	Authenticated bool      `json:"authenticated"`
	ObservedAt    time.Time `json:"observed_at"`
}

var ident = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
var secret = regexp.MustCompile("(?i)(bearer\\s+\\S+|(?:password|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie)\\s*[:=]\\s*\\S+|-----BEGIN.*PRIVATE KEY-----)")

func rank(tier string) int {
	switch tier {
	case "untrusted":
		return 1
	case "trusted":
		return 2
	case "local":
		return 3
	}
	return 0
}
func Validate(p Packet) error {
	for _, id := range []string{p.ThreadID, p.SessionTreeID, p.JobID} {
		if id != "" && !ident.MatchString(id) {
			return errors.New("HANDOFF_INVALID_ID")
		}
	}
	if p.PlanFingerprint != "" && p.PlanFingerprint != Fingerprint(p) {
		return errors.New("HANDOFF_PLAN_CHANGED")
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 16384 || p.Objective == "" || p.Scope == "" || len(p.HEAD) != 40 || !ident.MatchString(p.RunID) || p.Checkpoint == "" || p.NextSafeAction == "" || p.Owner == "" || len(p.Fields) > 32 || len(p.Receipts) > 32 || len(p.Blockers) > 16 {
		return errors.New("HANDOFF_INVALID")
	}
	if b, err := hex.DecodeString(p.HEAD); err != nil || len(b) != 20 {
		return errors.New("HANDOFF_INVALID_HEAD")
	}
	switch p.Stage {
	case "session_created", "session_loaded", "task_submitted", "work_completed", "code_reviewed", "integrated", "production_deployed", "UNKNOWN", "HOLD":
	default:
		return errors.New("HANDOFF_INVALID_STAGE")
	}
	for _, f := range p.Fields {
		if rank(f.MinTrust) == 0 {
			return errors.New("HANDOFF_INVALID_FIELD_TIER")
		}
	}
	return nil
}
func SetPolicy(db *statestore.DB, p Policy) error {
	if !ident.MatchString(p.Provider) || rank(p.Tier) == 0 {
		return errors.New("HANDOFF_EXPLICIT_TIER_REQUIRED")
	}
	raw, _ := json.Marshal(p)
	return db.Put("handoff/providers/"+p.Provider, raw)
}
func Save(db *statestore.DB, id string, p Packet) error {
	if !ident.MatchString(id) {
		return errors.New("HANDOFF_INVALID_ID")
	}
	if p.PlanFingerprint == "" {
		p.PlanFingerprint = Fingerprint(p)
	}
	if err := Validate(p); err != nil {
		return err
	}
	raw, _ := json.Marshal(p)
	ok, err := db.PutIfAbsentBounded("handoff/packets/"+id, raw, "handoff/packets/", 128)
	if err != nil {
		return err
	}
	if !ok {
		old, err := db.Get("handoff/packets/" + id)
		if err != nil || string(old) != string(raw) {
			return errors.New("HANDOFF_ID_CONFLICT")
		}
	}
	return nil
}
func Read(db *statestore.DB, id string) (Packet, error) {
	var p Packet
	if !ident.MatchString(id) {
		return p, errors.New("HANDOFF_INVALID_ID")
	}
	raw, err := db.Get("handoff/packets/" + id)
	if err != nil {
		return p, err
	}
	if json.Unmarshal(raw, &p) != nil || Validate(p) != nil {
		return p, errors.New("HANDOFF_CORRUPTED")
	}
	return p, nil
}

// ObserveAuthentication is called only by an authenticated provider adapter.
func ObserveAuthentication(db *statestore.DB, provider string, ok bool, now time.Time) error {
	if !ident.MatchString(provider) || now.IsZero() {
		return errors.New("HANDOFF_INVALID_PROVIDER_OBSERVATION")
	}
	if db == nil {
		return errors.New("HANDOFF_STATE_UNAVAILABLE")
	}
	key := "handoff/auth/" + provider
	next, _ := json.Marshal(authObservation{ok, now.UTC()})
	for range 4 {
		raw, err := db.Get(key)
		if errors.Is(err, os.ErrNotExist) {
			raw, err = nil, nil
		}
		if err != nil {
			return err
		}
		if len(raw) > 0 {
			var old authObservation
			if json.Unmarshal(raw, &old) != nil {
				return errors.New("HANDOFF_CORRUPTED")
			}
			// A replayed task cannot undo a later revocation; revocation wins ties.
			if old.ObservedAt.After(now) || old.ObservedAt.Equal(now) && (!old.Authenticated || ok) {
				return nil
			}
		}
		var changed bool
		if len(raw) == 0 {
			changed, err = db.PutIfAbsent(key, next)
		} else {
			changed, err = db.Replace(key, raw, next)
		}
		if err != nil {
			return err
		}
		if changed {
			return nil
		}
	}
	return errors.New("HANDOFF_AUTH_CHANGED")
}

func Export(db *statestore.DB, id, provider string, now time.Time) (map[string]any, error) {
	if !ident.MatchString(provider) {
		return nil, errors.New("HANDOFF_INVALID_PROVIDER")
	}
	var policy Policy
	raw, err := db.Get("handoff/providers/" + provider)
	if err != nil || json.Unmarshal(raw, &policy) != nil || policy.Provider != provider || rank(policy.Tier) == 0 {
		return nil, errors.New("HANDOFF_EXPLICIT_TIER_REQUIRED")
	}
	var auth authObservation
	raw, err = db.Get("handoff/auth/" + provider)
	if err != nil || json.Unmarshal(raw, &auth) != nil || !auth.Authenticated || auth.ObservedAt.IsZero() || auth.ObservedAt.After(now) || now.Sub(auth.ObservedAt) > 5*time.Minute {
		return nil, errors.New("HANDOFF_AUTH_UNKNOWN")
	}
	p, err := Read(db, id)
	if err != nil {
		return nil, err
	}
	// Operational identity survives every tier; private topology never reaches untrusted.
	out := map[string]any{"trust": "UNTRUSTED", "run_id": p.RunID, "stage": p.Stage, "plan_fingerprint": p.PlanFingerprint, "thread_id": p.ThreadID, "session_tree_id": p.SessionTreeID, "job_id": p.JobID, "generation": p.Generation, "dispatch_enabled": false, "retry_allowed": false}
	values := map[string]Field{"objective": {p.Objective, "trusted", false}, "scope": {p.Scope, "trusted", false}, "head": {p.HEAD, "untrusted", false}, "owner": {p.Owner, "trusted", false}, "lease_id": {p.LeaseID, "local", false}, "checkpoint": {p.Checkpoint, "trusted", false}, "next_safe_action": {p.NextSafeAction, "trusted", false}}
	for k, v := range p.Fields {
		if _, reserved := out[k]; !reserved {
			if _, built := values[k]; !built {
				values[k] = v
			}
		}
	}
	for k, f := range values {
		key := strings.ToLower(k)
		if policy.Tier == "untrusted" && (strings.Contains(key, "infra") || strings.Contains(key, "topology") || strings.Contains(key, "user_model")) {
			continue
		}
		if strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "cookie") || strings.Contains(key, "credential") {
			continue
		}
		if !f.Secret && rank(f.MinTrust) <= rank(policy.Tier) && !secret.MatchString(f.Value) && !strings.Contains(strings.ToLower(k), "token") && !strings.Contains(strings.ToLower(k), "key") {
			out[k] = f.Value
		}
	}
	if policy.Tier != "untrusted" {
		safe := func(in []string) []string {
			out := []string{}
			for _, v := range in {
				if !secret.MatchString(v) {
					out = append(out, v)
				}
			}
			return out
		}
		out["receipts"] = safe(p.Receipts)
		out["blockers"] = safe(p.Blockers)
	}
	return out, nil
}

// Switch changes a label only, preserving exact identities and every gate.
func Switch(db *statestore.DB, id, provider string, planFingerprint string, independentOrFenced bool) (Packet, error) {
	p, err := Read(db, id)
	if err != nil {
		return p, err
	}
	if !ident.MatchString(provider) || !independentOrFenced || p.PlanFingerprint == "" || p.PlanFingerprint != planFingerprint {
		return p, errors.New("HANDOFF_SWITCH_HOLD")
	}
	before, _ := json.Marshal(p)
	p.Provider = provider
	after, _ := json.Marshal(p)
	ok, err := db.Replace("handoff/packets/"+id, before, after)
	if err != nil || !ok {
		return p, errors.New("HANDOFF_SWITCH_CONFLICT")
	}
	return p, nil
}
func Fingerprint(p Packet) string {
	raw, _ := json.Marshal([]string{p.Objective, p.Scope, p.HEAD, p.RunID, p.CreationKey, p.LoadKey})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
