package projectlease

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"mcpshka/internal/deploy"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
)

func recoveryHold(code, root, message string, cause error) error {
	e := failure.NewHold(failure.EvidenceMissing, code, "projectlease.recovery", message, cause)
	e.NextSafeAction = failure.Action{Tool: "project_context", Arguments: map[string]any{"path": root}}
	return e
}

// Read durable job authority, including queued work and isolated executions
// that have no local PID. A process scan alone cannot see those writers.
func (s *Service) recoveryQuiescent(root string) error {
	if s.database == nil {
		return recoveryHold("job_authority_unavailable", root, "SQLite job authority required", nil)
	}
	seen := 0
	err := s.database.Scan("jobs/", func(key string, raw []byte) error {
		if seen++; seen > 4096 {
			return errors.New("job evidence budget exceeded")
		}
		var j jobs.Snapshot
		if !strings.HasSuffix(key, "/metadata.json") || json.Unmarshal(raw, &j) != nil || j.Workdir == "" {
			return errors.New("job evidence unreadable")
		}
		if j.Workdir != root && !strings.HasPrefix(j.Workdir, root+"/") {
			return nil
		}
		gone, err := jobs.RecoveryResourcesGone(j)
		if err != nil {
			return err
		}
		if gone {
			return nil
		}
		return errors.New("project job pending or resources not verified released: " + j.ID)
	})
	if err != nil {
		return recoveryHold("project_jobs_unverified", root, "reconcile project jobs before transfer", err)
	}
	pending, err := deploy.New(s.policy, filepath.Join(filepath.Dir(s.dir), "deploy")).Pending()
	if err != nil || pending {
		return recoveryHold("deployment_state_unverified", root, "deployment authority unavailable or pending", err)
	}
	return nil
}

// An unbound remote owner still needs the independent sealed OS boundary.
// A bound owner must additionally be proved gone, even without open files.
func recoveryBoundOwner(r record) error {
	if r.OwnerPID == 0 && r.OwnerStartTicks == 0 && r.OwnerBootID == "" {
		return nil
	}
	gone, err := (jobs.OwnerIdentity{PID: r.OwnerPID, StartTicks: r.OwnerStartTicks, BootID: r.OwnerBootID}).Gone()
	if err != nil || !gone {
		return recoveryHold("owner_identity_unverified", r.ProjectRoot, "bound owner must be verified gone", err)
	}
	return nil
}
