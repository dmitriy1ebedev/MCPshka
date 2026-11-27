package codexcoord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type Intent struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Worktree   string `json:"worktree"`
	RoadmapSHA string `json:"roadmap_sha256"`
	Message    string `json:"message"`
	Model      string `json:"model"`
	Effort     string `json:"reasoning_effort"`
	State      string `json:"state"`
	Revision   uint64 `json:"revision"`
}

type IntentSummary struct {
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	WorktreeSHA256  string `json:"worktree_sha256"`
	RoadmapSHA      string `json:"roadmap_sha256"`
	MessageSHA256   string `json:"message_sha256"`
	Model           string `json:"model"`
	Effort          string `json:"reasoning_effort"`
	State           string `json:"state"`
	Revision        uint64 `json:"revision"`
	CatalogVerified bool   `json:"catalog_verified"`
	DispatchEnabled bool   `json:"dispatch_enabled"`
	NextAction      string `json:"next_action"`
}

func hash(s string) string       { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func intentKey(id string) string { return "codexcoord/intents/" + id }
func validEffort(e string) bool {
	switch e {
	case "", "minimal", "low", "medium", "high", "xhigh":
		return true
	}
	return false
}
func validIntent(i Intent) error {
	return ValidateShape(Task{ID: i.ID, Provider: "codex", SessionID: i.SessionID,
		Worktree: i.Worktree, RoadmapSHA: i.RoadmapSHA, Message: i.Message, Model: i.Model, Effort: i.Effort})
}

// Summarize deliberately excludes task text, unverified authorization and
// any claim of real Codex dispatch or supported model capability.
func Summarize(i Intent) IntentSummary {
	return summarizeIntent(i)
}

func summarizeIntent(i Intent) IntentSummary {
	next := "trusted_catalog_and_scoped_owner_verification_required"
	if i.State == "cancelled" {
		next = "cancelled_no_dispatch"
	}
	return IntentSummary{
		ID: i.ID, SessionID: i.SessionID, WorktreeSHA256: hash(i.Worktree), RoadmapSHA: i.RoadmapSHA,
		MessageSHA256: hash(i.Message), Model: i.Model, Effort: i.Effort, State: i.State,
		Revision: i.Revision, CatalogVerified: false, DispatchEnabled: false, NextAction: next,
	}
}
func (q *Queue) ReadIntent(id string) (Intent, error) {
	if !validID.MatchString(id) {
		return Intent{}, errors.New("invalid intent id")
	}
	raw, e := q.db.Get(intentKey(id))
	if e != nil {
		return Intent{}, e
	}
	var i Intent
	if e = json.Unmarshal(raw, &i); e != nil {
		return Intent{}, e
	}
	if i.ID != id || i.Revision == 0 || (i.State != "draft" && i.State != "cancelled") || validIntent(i) != nil {
		return Intent{}, errors.New("invalid durable intent receipt")
	}
	return i, nil
}

// StageIntent is a durable non-executable request. The claimed model is ONLY a
// preference: it has NOT been checked against the live Codex model catalog.
func (q *Queue) StageIntent(i Intent) (IntentSummary, error) {
	if i.State != "" || i.Revision != 0 {
		return IntentSummary{}, errors.New("caller supplied execution state")
	}
	if e := validIntent(i); e != nil {
		return IntentSummary{}, e
	}
	i.State = "draft"
	i.Revision = 1
	raw, e := json.Marshal(i)
	if e != nil {
		return IntentSummary{}, e
	}
	created, e := q.db.PutIfAbsentBounded(intentKey(i.ID), raw, "codexcoord/intents/", 128)
	if e != nil {
		return IntentSummary{}, e
	}
	if created {
		return summarizeIntent(i), nil
	}
	old, e := q.ReadIntent(i.ID)
	if errors.Is(e, os.ErrNotExist) {
		return IntentSummary{}, errors.New("intent capacity reached: HOLD")
	}
	if e != nil {
		return IntentSummary{}, e
	}
	if old != i {
		return IntentSummary{}, errors.New("intent idempotency conflict")
	}
	return summarizeIntent(old), nil
}
func (q *Queue) ChangeIntent(id string, revision uint64, model, effort string) (IntentSummary, error) {
	if !validModel.MatchString(model) || !validEffort(effort) {
		return IntentSummary{}, errors.New("unrecognized model request")
	}
	i, e := q.ReadIntent(id)
	if e != nil {
		return IntentSummary{}, e
	}
	if revision == ^uint64(0) || i.Revision != revision || i.State != "draft" {
		return IntentSummary{}, errors.New("stale or closed intent: HOLD")
	}
	before, _ := json.Marshal(i)
	i.Model = model
	i.Effort = effort
	i.Revision++
	after, _ := json.Marshal(i)
	ok, e := q.db.Replace(intentKey(id), before, after)
	if e != nil {
		return IntentSummary{}, e
	}
	if !ok {
		return IntentSummary{}, errors.New("concurrent intent change: HOLD")
	}
	return summarizeIntent(i), nil
}
func (q *Queue) CancelIntent(id string, revision uint64) (IntentSummary, error) {
	i, e := q.ReadIntent(id)
	if e != nil {
		return IntentSummary{}, e
	}
	if i.State == "cancelled" && i.Revision == revision {
		return summarizeIntent(i), nil
	}
	if revision == ^uint64(0) || i.Revision != revision || i.State != "draft" {
		return IntentSummary{}, errors.New("stale or closed intent: HOLD")
	}
	before, _ := json.Marshal(i)
	i.State = "cancelled"
	i.Revision++
	after, _ := json.Marshal(i)
	ok, e := q.db.Replace(intentKey(id), before, after)
	if e != nil {
		return IntentSummary{}, e
	}
	if !ok {
		return IntentSummary{}, errors.New("concurrent cancel: HOLD")
	}
	return summarizeIntent(i), nil
}
func (q *Queue) ListIntents(limit int) ([]IntentSummary, error) {
	if limit < 1 || limit > 50 {
		return nil, errors.New("intent list limit must be 1..50")
	}
	keys, e := q.db.List("codexcoord/intents/")
	if e != nil {
		return nil, e
	}
	if len(keys) > 128 {
		return nil, errors.New("intent scan budget: HOLD")
	}
	out := make([]IntentSummary, 0, limit)
	for _, key := range keys {
		i, e := q.ReadIntent(strings.TrimPrefix(key, "codexcoord/intents/"))
		if e != nil {
			return nil, e
		}
		out = append(out, summarizeIntent(i))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
