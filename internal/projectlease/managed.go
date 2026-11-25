package projectlease

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/deploy"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
)

type Recovery struct {
	ProjectRoot string `json:"project_root"`
	OwnerJobID  string `json:"owner_job_id,omitempty"`
	Released    bool   `json:"released"`
	Hold        string `json:"hold,omitempty"`
}

// Managed leases belong exclusively to one deterministic bounded workflow job,
// never to an interactive chat or to the MCP daemon PID.
func (s *Service) AcquireManaged(path, executionID string, m *jobs.Manager) (AcquireResult, error) {
	if s.database == nil || s.stateErr != nil {
		return AcquireResult{}, errors.New("managed leases require SQLite authority")
	}
	if !validExecutionID(executionID) {
		return AcquireResult{}, errors.New("invalid execution identity")
	}
	root, err := s.projectRoot(path)
	if err != nil {
		return AcquireResult{}, err
	}
	var out AcquireResult
	err = m.WithIdleProject(root, func() error {
		var e error
		out, e = s.acquire(root, "workflow:"+executionID, executionID, "workflow-"+executionID, nil)
		return e
	})
	return out, err
}

// WithInteractiveHolder admits only the current tracked interactive owner for
// one synchronous filesystem effect. It never admits tokenless jobs or managed
// lease tokens. Local release is serialized with the effect; a different
// daemon still needs the root writer gate and SQLite holder proof.
func (s *Service) WithInteractiveHolder(path, token string, effect func() error) error {
	return s.WithInteractiveHolders([]string{path}, token, effect)
}

func (s *Service) WithInteractiveHolders(paths []string, token string, effect func() error) error {
	if token == "" || effect == nil || s.database == nil || s.stateErr != nil {
		return errors.New("tracked interactive holder and SQLite authority required: HOLD")
	}
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	roots := []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		root, err := s.projectRoot(path)
		if err != nil {
			return err
		}
		if !seen[root] {
			roots = append(roots, root)
			seen[root] = true
		}
	}
	sort.Strings(roots)
	for _, root := range roots {
		fence, err := s.lockInteractiveFence(root)
		if err != nil {
			return err
		}
		defer fence.Close()
		if err := s.writerAdmission(root); err != nil {
			return err
		}
		holder, err := s.ValidateHolder(root, token)
		if err != nil {
			return err
		}
		if err := holderReconcile(holder); err != nil {
			return err
		}
	}
	return effect()
}

// GuardHolder admits a job for the exact current interactive holder. The
// holder already owns the project's writes; workflow and deployment fencing
// still apply through the caller's pipeline guard.
func (s *Service) GuardHolder(path, token string) error {
	if token == "" || s.database == nil || s.stateErr != nil {
		return errors.New("tracked interactive holder and SQLite authority required: HOLD")
	}
	root, err := s.projectRoot(path)
	if err != nil {
		return err
	}
	if err := s.writerAdmission(root); err != nil {
		return err
	}
	holder, err := s.ValidateHolder(root, token)
	if err != nil {
		return err
	}
	if err := holderReconcile(holder); err != nil {
		return err
	}
	return nil
}

// Guard applies at the shared native-job admission point. Reserved identities
// are internal only; ordinary exec_start cannot impersonate a managed owner.
func (s *Service) Guard(path, reservedID string) error {
	root, err := s.projectRoot(path)
	if err != nil {
		return err
	}
	if err := s.writerAdmission(root); err != nil {
		return err
	}
	r, err := s.readRecord(s.leasePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.OwnerJobID == "" {
		return errors.New("interactive or legacy project lease held: HOLD")
	}
	if reservedID != r.OwnerJobID || r.Owner != "workflow:"+r.ExecutionID || !validExecutionID(r.ExecutionID) {
		return errors.New("managed project writer is active or owner identity invalid: HOLD")
	}
	return nil
}

func (s *Service) RecoverManaged(path string, m *jobs.Manager, deployments *deploy.Service) (Recovery, error) {
	root, err := s.projectRoot(path)
	if err != nil {
		return Recovery{}, err
	}
	out := Recovery{ProjectRoot: root}
	if s.database == nil || s.stateErr != nil {
		out.Hold = "SQLite authority unavailable"
		return out, nil
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	previous, err := s.database.Get(key)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var r record
	if err := json.Unmarshal(previous, &r); err != nil {
		return out, err
	}
	out.OwnerJobID = r.OwnerJobID
	if r.OwnerJobID != "workflow-"+r.ExecutionID || !validExecutionID(r.ExecutionID) || r.Owner == "" || r.Token == "" || r.ProjectRoot != root || r.AcquiredAt.IsZero() || r.HeartbeatAt.IsZero() {
		out.Hold = "unbound or legacy owner: explicit holder/admin reconciliation required"
		return out, nil
	}
	if r.HeartbeatAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		out.Hold = "future heartbeat: HOLD"
		return out, nil
	}
	job, err := m.Get(r.OwnerJobID)
	if err != nil {
		out.Hold = "owner job unavailable: outcome UNKNOWN"
		return out, nil
	}
	jobRoot, err := s.projectRoot(job.Workdir)
	if err != nil || jobRoot != root {
		out.Hold = "owner job is outside the project"
		return out, nil
	}
	// MCP-265: an external (workbench) job has no local PID; its runtime
	// binding is the identity and WithGoneOwner demands terminal + fenced.
	external := job.External != nil
	if !external && (job.PID <= 0 || job.StartTicks == 0 || job.BootID == "") {
		out.Hold = "owner PID/start-time/boot binding unavailable"
		return out, nil
	}
	if r.OwnerPID != 0 && (r.OwnerPID != job.PID || r.OwnerStartTicks != job.StartTicks || r.OwnerBootID != job.BootID) {
		out.Hold = "owner process identity changed"
		return out, nil
	}
	if r.OwnerPID == 0 && !external {
		r.OwnerPID, r.OwnerStartTicks, r.OwnerBootID = job.PID, job.StartTicks, job.BootID
		replacement, e := json.Marshal(r)
		if e != nil {
			return out, e
		}
		replacement = append(replacement, '\n')
		ok, e := s.database.ReplaceUnlessGate(key, previous, replacement, s.unprivilegedWriterGate(root))
		if e != nil {
			return out, e
		}
		if !ok {
			out.Hold = "lease changed while binding owner"
			return out, nil
		}
		previous = replacement
	}
	if job.State == jobs.Queued || job.State == jobs.Running || job.State == jobs.Cancelling {
		if _, err := s.HeartbeatRefresh(root, r.Token); err != nil {
			return out, err
		}
		out.Hold = "owner job active"
		return out, nil
	}
	err = m.WithGoneOwner(root, r.OwnerJobID, func(bound jobs.Snapshot) error {
		if bound.PID != r.OwnerPID || bound.StartTicks != r.OwnerStartTicks || bound.BootID != r.OwnerBootID {
			return errors.New("owner binding changed: HOLD")
		}
		pending, e := deployments.Pending()
		if e != nil {
			return e
		}
		if pending {
			return errors.New("deployment pending: HOLD")
		}
		if logger := m.AuditLogger(); logger != nil {
			if e := logger.Log(audit.Event{Type: "project_lease_recovery", Workdir: root, JobID: r.OwnerJobID, Result: "owner resources verified gone"}); e != nil {
				return e
			}
		}
		ok, e := s.database.Delete(key, previous)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("lease changed during recovery: HOLD")
		}
		out.Released = true
		if logger := m.AuditLogger(); logger != nil {
			_ = logger.Log(audit.Event{Type: "project_lease_recovery", Workdir: root, JobID: r.OwnerJobID, Result: "released"})
		}
		return nil
	})
	if err != nil {
		out.Hold = err.Error()
	}
	return out, nil
}

func (s *Service) MaintainManaged(m *jobs.Manager, deployments *deploy.Service) {
	if s.database == nil || s.stateErr != nil {
		return
	}
	// One paged scan replaces a listing plus one SQLite round trip per lease.
	var roots []string
	seen := 0
	err := s.database.Scan("leases/", func(_ string, data []byte) error {
		if seen++; seen > 128 {
			return errLeaseScanBudget
		}
		var r record
		if json.Unmarshal(data, &r) == nil && r.OwnerJobID != "" {
			roots = append(roots, r.ProjectRoot)
		}
		return nil
	})
	if err != nil {
		return
	}
	for i, root := range roots {
		if i >= 16 {
			return
		}
		rec, err := s.RecoverManaged(root, m, deployments)
		if err != nil && rec.Hold == "" {
			rec.Hold = err.Error()
		}
		s.noteHold(root, rec.Hold, m)
	}
}

var errLeaseScanBudget = errors.New("lease scan budget exceeded")

// noteHold remembers why a managed lease is not released and audits each
// new reason once (MCP-265: a silent hold left owners guessing).
func (s *Service) noteHold(root, hold string, m *jobs.Manager) {
	holdMu.Lock()
	prev := holds[root]
	if hold == "" || hold == "owner job active" {
		delete(holds, root)
	} else {
		holds[root] = hold
	}
	holdMu.Unlock()
	if hold != prev && hold != "" && hold != "owner job active" {
		if logger := m.AuditLogger(); logger != nil {
			_ = logger.Log(audit.Event{Type: "project_lease_recovery", Workdir: root, Result: "hold: " + hold})
		}
	}
}

// RecoveryHold is the last reason the managed lease of root was kept.
func (s *Service) RecoveryHold(root string) string {
	holdMu.Lock()
	defer holdMu.Unlock()
	return holds[root]
}

// Every Service instance of the daemon shares the maintenance view.
var (
	holdMu sync.Mutex
	holds  = map[string]string{}
)

// ReferencedJobs returns job IDs that own a managed lease. Their receipts stay
// authoritative for recovery, so the job reaper must keep them.
func (s *Service) ReferencedJobs() (map[string]bool, error) {
	if s.stateErr != nil {
		return nil, s.stateErr
	}
	if s.database == nil {
		return nil, errors.New("SQLite authority unavailable")
	}
	out := map[string]bool{}
	err := s.database.Scan("leases/", func(_ string, data []byte) error {
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.OwnerJobID != "" {
			out[r.OwnerJobID] = true
		}
		return nil
	})
	return out, err
}

// holderReconcile names why a holder cannot act. A silent holder is the
// common case (the agent paused longer than the liveness window): one
// heartbeat with its own token clears it, which the bare "requires
// reconciliation" HOLD never said (agents stopped on HOLD_REASON_UNCLASSIFIED).
func holderReconcile(h Status) error {
	switch {
	case h.ReconciliationRequired && h.OwnerJobID == "" && validExecutionID(h.ExecutionID) && h.Generation != 0:
		// Nothing started: the retry key stays usable after the heartbeat
		// (BUG-20261103-5; project_test pt-032-01 was burnt this way).
		e := failure.RejectedAs(failure.OwnerBusy, "LEASE_HEARTBEAT_STALE", "projectlease.holderReconcile", "lease heartbeat is stale: call project_lease_heartbeat with this lease token, then retry with the same key", nil)
		e.NextSafeAction = failure.Action{Tool: "project_lease_heartbeat", Arguments: map[string]any{}}
		return e
	case h.OwnerJobID != "" || !validExecutionID(h.ExecutionID) || h.Generation == 0 || h.ReconciliationRequired:
		return errors.New("interactive holder identity requires reconciliation: HOLD")
	}
	return nil
}

// ReleaseIdle serializes holder cleanup with native job admission. Missing
// deployment evidence or pending resources never authorize another writer.
func (s *Service) ReleaseIdle(path, token string, m *jobs.Manager, deployments *deploy.Service) (out ReleaseResult, err error) {
	if token == "" || m == nil || deployments == nil {
		return out, failure.Rejected("LEASE_RELEASE_UNVERIFIED", "projectlease.ReleaseIdle", "current holder and idle-project authorities required; nothing released", nil)
	}
	root, err := s.releaseRoot(path)
	if err != nil {
		return out, failure.Rejected("LEASE_RELEASE_PATH", "projectlease.ReleaseIdle", "project could not be verified; nothing released", err)
	}
	err = m.WithIdleProject(root, func() error {
		// File-only leases cannot authorize interactive work; preserve their token-checked cleanup.
		if s.database != nil || m.Database() != nil {
			pending, err := deployments.Pending()
			if err != nil || pending {
				return failure.Rejected("LEASE_RELEASE_DEPLOYMENT", "projectlease.ReleaseIdle", "deployment pending or unverified; nothing released", err)
			}
		}
		out, err = s.Release(root, token)
		return err
	})
	return out, err
}
