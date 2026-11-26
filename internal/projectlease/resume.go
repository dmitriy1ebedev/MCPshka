package projectlease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
)

type ResumeInput struct {
	Path           string `json:"path"`
	Token          string `json:"token" jsonschema:"Current holder secret supplied through a trusted handoff; never infer authority from owner names."`
	Owner          string `json:"owner"`
	IdempotencyKey string `json:"idempotency_key"`
}

type resumeJournal struct {
	RequestSHA256 string `json:"request_sha256"`
	Previous      Status `json:"previous"`
	Successor     Status `json:"successor"`
}

// Resume is a voluntary credential handoff. The previous owner must stop
// writing before sharing its secret; a related display name proves nothing.
func (s *Service) Resume(in ResumeInput, m *jobs.Manager, deployments *deploy.Service, guard func(string, string) error) (AcquireResult, error) {
	if in.Token == "" || strings.TrimSpace(in.Owner) == "" || len(in.Owner) > 128 || in.IdempotencyKey == "" || m == nil || deployments == nil || guard == nil || s.database == nil {
		return AcquireResult{}, errors.New("current holder, idle project and durable authority required")
	}
	root, err := s.projectRoot(in.Path)
	if err != nil {
		return AcquireResult{}, err
	}
	in.Path = root
	request, _ := json.Marshal(in)
	sum := sha256.Sum256(request)
	digest := hex.EncodeToString(sum[:])
	journalKey := "project-lease-resumes/v1/" + digest
	var out AcquireResult
	err = m.WithIdleProject(root, func() error {
		if err := guard(root, ""); err != nil {
			return err
		}
		pending, err := deployments.Pending()
		if err != nil || pending {
			return recoveryHold("deployment_state_unverified", root, "reconcile deployment before resume", err)
		}
		if raw, err := s.database.Get(journalKey); err == nil {
			var j resumeJournal
			if json.Unmarshal(raw, &j) != nil || j.RequestSHA256 != digest {
				return errors.New("resume journal invalid: HOLD")
			}
			current, err := s.readRecord(s.leasePath(root))
			if err != nil {
				return err
			}
			if current.Owner != j.Successor.Owner || current.ExecutionID != j.Successor.ExecutionID || current.Generation != j.Successor.Generation || leaseIdentity(current) != j.Successor.LeaseID {
				return errors.New("resume receipt is stale: reconcile current holder")
			}
			out = AcquireResult{Status: publicStatus(current), Token: current.Token}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		// A stale heartbeat can be refreshed by the actual token holder first.
		return s.WithInteractiveHolder(root, in.Token, func() error {
			key := "leases/" + filepath.Base(s.leasePath(root))
			raw, err := s.database.Get(key)
			if err != nil {
				return err
			}
			var old record
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if old.Generation == ^uint64(0) {
				return errors.New("lease generation exhausted")
			}
			token, err := randomToken()
			if err != nil {
				return err
			}
			execution, err := randomToken()
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			next := record{LeaseID: leaseIdentity(old), Generation: old.Generation + 1, ProjectRoot: root, CommonGitDir: old.CommonGitDir, Owner: strings.TrimSpace(in.Owner), Token: token, ExecutionID: execution[:32], AcquiredAt: now, HeartbeatAt: now}
			replacement, _ := json.Marshal(next)
			journal, _ := json.Marshal(resumeJournal{RequestSHA256: digest, Previous: publicStatus(old), Successor: publicStatus(next)})
			ok, err := s.database.ReplaceWithJournal(key, raw, replacement, journalKey, journal)
			if err != nil {
				e := failure.NewHold(failure.FencedUnknown, "RESUME_OUTCOME_UNKNOWN", "projectlease.Resume", "resume CAS outcome UNKNOWN; reconcile then repeat identical request", err)
				e.NextSafeAction = failure.Action{Tool: "project_lease_status", Arguments: map[string]any{"path": root}}
				return e
			}
			if !ok {
				return errors.New("lease changed during resume: HOLD")
			}
			out = AcquireResult{Status: publicStatus(next), Token: token}
			return nil
		})
	})
	return out, err
}
