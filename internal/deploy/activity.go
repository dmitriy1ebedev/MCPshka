package deploy

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"mcpshka/internal/statestore"
)

type Reconciliation struct {
	Pending                bool     `json:"pending"`
	NonterminalCount       int      `json:"nonterminal_count"`
	FencedCount            int      `json:"fenced_count"`
	RetiredCount           int      `json:"retired_count"`
	ReconciliationRequired bool     `json:"reconciliation_required"`
	Deployments            []Status `json:"deployments"`
	Truncated              bool     `json:"truncated"`
	Trust                  string   `json:"trust"`
}

func terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "rolled_back"
}

// Observation never rewrites helper authority or treats age as proof of failure.
func observeStatus(s Status, now time.Time) (Status, error) {
	if !validID(s.DeploymentID) || s.State == "" {
		return Status{}, errors.New("invalid deployment status: HOLD")
	}
	s.CorrelationID = s.DeploymentID
	s.Blocking, s.Fenced, s.FenceSHA256 = !terminal(s.State), false, ""
	s.Retired = false
	s.ReconciliationRequired, s.ReasonCode, s.NextAction = false, "", ""
	if terminal(s.State) {
		s.NextAction = "verify_production_and_rollback_identity"
		return s, nil
	}
	s.NextAction = "inspect_detached_unit_and_production_identity_do_not_replay"
	started, err := time.Parse(time.RFC3339Nano, s.StartedAt)
	switch {
	case err != nil || started.After(now):
		s.ReasonCode = "DEPLOYMENT_TIME_UNKNOWN"
	case now.Sub(started) >= 5*time.Minute:
		s.ReasonCode = "STALE_DEPLOYMENT_UNKNOWN"
	case s.State != "accepted" && s.State != "running":
		s.ReasonCode = "DEPLOYMENT_OUTCOME_UNKNOWN"
	}
	s.ReconciliationRequired = s.ReasonCode != ""
	return s, nil
}

// Reconcile returns bounded durable evidence, never permission to retry a mutation.
const maxDeploymentRecords = 4096

func (s *Service) Reconcile() (Reconciliation, error) {
	out := Reconciliation{Deployments: []Status{}, Trust: "UNTRUSTED"}
	db, err := statestore.Active(filepath.Dir(s.stateDir))
	if err != nil {
		return out, err
	}
	if db == nil {
		return out, errors.New("deployment verification requires SQLite authority: HOLD")
	}
	now := time.Now().UTC()
	remaining := 16 * 1024
	count := 0
	// One paged scan instead of a sqlite3 process per record: history grows
	// with every release, and a fixed 128-record cap would block admission.
	err = db.Scan("deploys/", func(key string, raw []byte) error {
		if count++; count > maxDeploymentRecords {
			return errors.New("deployment verification budget exceeded: HOLD")
		}
		if len(raw) > 64*1024 {
			return errors.New("deployment status exceeds limit: HOLD")
		}
		var state Status
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		if key != "deploys/"+state.DeploymentID+".json" {
			return errors.New("deployment status id mismatch: HOLD")
		}
		state, err := observeStoredStatus(state, raw, now)
		if err != nil {
			return err
		}
		if terminal(state.State) {
			return nil
		}
		return out.add(state, &remaining)
	})
	return out, err
}

// add accepts only statuses already observed against the stored root fence.
func (out *Reconciliation) add(state Status, remaining *int) error {
	out.Pending = out.Pending || state.Blocking
	if state.Fenced {
		out.FencedCount++
	}
	if state.Retired {
		out.RetiredCount++
	} else {
		out.NonterminalCount++
	}
	// A verified permanent fence retires active recovery, not the unknown audit outcome.
	out.ReconciliationRequired = out.ReconciliationRequired || (state.Blocking && state.ReconciliationRequired)
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(out.Deployments) < 16 && len(encoded) <= *remaining {
		out.Deployments = append(out.Deployments, state)
		*remaining -= len(encoded)
	} else {
		out.Truncated = true
	}
	return nil
}

// Failure to enumerate/decode authority is a blocker, never evidence of idleness.
func (s *Service) Pending() (bool, error) {
	out, err := s.Reconcile()
	return out.Pending, err
}
