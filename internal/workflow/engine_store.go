package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"mcpshka/internal/jobs"
)

func (e *Engine) Get(id string) (Run, error) {
	if e.db == nil {
		return Run{}, errors.New("durable SQLite authority unavailable")
	}
	if !validID(id) {
		return Run{}, errors.New("invalid pipeline ID")
	}
	raw, err := e.db.Get(runKey(id))
	if err != nil {
		return Run{}, err
	}
	var r Run
	if err = decodeRun(raw, id, &r); err != nil {
		return Run{}, err
	}
	return r, nil
}

func decodeRun(raw []byte, id string, r *Run) error {
	if len(raw) > 65536 || json.Unmarshal(raw, r) != nil || r.ID != id ||
		!validID(r.ID) || !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root ||
		r.Pipeline == "" || len(r.Pipeline) > 64 || r.Scope == "" || len(r.Scope) > 64 || !validDigest(r.Revision, 40) ||
		!validDigest(r.RoadmapSHA, 64) || !validDigest(r.Fingerprint, 64) ||
		len(r.Plans) != 12 || r.Index < 0 || r.Index >= len(r.Plans) {
		return errors.New("invalid durable workflow snapshot: HOLD")
	}
	if r.MissionID != "" && !validDigest(r.MissionID, 32) || r.Deadline != nil && r.Deadline.IsZero() {
		return errors.New("invalid mission workflow binding: HOLD")
	}
	switch r.Control {
	case "", "paused", "cancelled", "expired":
	default:
		return errors.New("invalid mission control: HOLD")
	}
	if (r.Control != "" || r.Deadline != nil) && r.MissionID == "" {
		return errors.New("unbound mission control: HOLD")
	}
	if r.Schema != "" && r.Schema != runSchema || r.Schema == "" && r.MissionID != "" {
		return errors.New("unsupported durable workflow schema: HOLD")
	}
	plans, err := json.Marshal(r.Plans)
	if err != nil || planDigest(plans, r.Schema) != r.Fingerprint {
		return errors.New("durable workflow plan fingerprint mismatch: HOLD")
	}
	for _, plan := range r.Plans {
		pinned := plan.Fingerprint
		plan.Fingerprint = ""
		rawPlan, err := json.Marshal(plan)
		if err != nil || digest(rawPlan) != pinned || plan.Root != r.Root ||
			plan.Command == "" || plan.TimeoutSeconds <= 0 || plan.OutputLimitBytes <= 0 {
			return errors.New("invalid durable workflow stage: HOLD")
		}
	}
	switch r.State {
	case "pending", "running", "reconciling", "hold":
	case "rollback":
		if r.Index != 10 {
			return errors.New("invalid rollback stage: HOLD")
		}
	case "finalizing":
		if r.Index != 11 {
			return errors.New("invalid finalizer stage: HOLD")
		}
	case "done":
		if r.Index != 9 || r.LaunchClaimed {
			return errors.New("unverified workflow completion: HOLD")
		}
	case "cancelled":
		if r.Index != 0 || r.LaunchClaimed || !r.LaunchAt.IsZero() || !stopped(*r) {
			return errors.New("unproven unstarted cancellation: HOLD")
		}
	case "rolled_back":
		if r.Index != 11 || r.LaunchClaimed {
			return errors.New("unverified recovery completion: HOLD")
		}
	default:
		return errors.New("unknown durable workflow state: HOLD")
	}
	if r.Index >= 10 {
		cause := r.Recovery
		if cause == nil || cause.Index < 0 || cause.Index > 9 || cause.JobID != stageJobID(r.ID, cause.Index) ||
			(cause.MissingReceipt && (cause.Supervisor.PID <= 0 || cause.Supervisor.StartTicks == 0 || cause.Supervisor.BootID == "")) {
			return errors.New("unverified recovery cause: HOLD")
		}
		if cause.Stopped && !stopped(*r) {
			return errors.New("unbound stop compensation: HOLD")
		}
	} else if r.Recovery != nil {
		return errors.New("unexpected recovery cause: HOLD")
	}
	if r.LaunchClaimed {
		if r.JobID != stageJobID(r.ID, r.Index) ||
			r.LaunchAt.IsZero() || (r.LaunchProtocol != "" && r.LaunchProtocol != jobs.ManagedLaunchProtocol) {
			return errors.New("invalid durable workflow launch: HOLD")
		}
		if r.LaunchProtocol == jobs.ManagedLaunchProtocol &&
			(r.Supervisor.PID <= 0 || r.Supervisor.StartTicks == 0 || r.Supervisor.BootID == "") {
			return errors.New("unbound durable workflow supervisor: HOLD")
		}
	} else if r.JobID != "" || r.State == "running" || r.State == "reconciling" {
		return errors.New("missing durable workflow launch: HOLD")
	}
	return nil
}

func validDigest(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func stageJobID(id string, index int) string {
	return "workflow-" + digest([]byte(fmt.Sprintf("%s:%d", id, index)))[:32]
}

func (e *Engine) verifyPrerequisites(r Run) error {
	start := 0
	if r.Index >= 10 {
		start = 10
	}
	for i := start; i < r.Index; i++ {
		job, err := e.manager.Get(stageJobID(r.ID, i))
		plan := r.Plans[i]
		args, _ := json.Marshal(job.Args)
		want, _ := json.Marshal(plan.Args)
		if err != nil || job.State != jobs.Done || job.ExitCode == nil || *job.ExitCode != 0 ||
			job.Workdir != r.Root || job.Command != plan.Command || string(args) != string(want) {
			return errors.New("workflow prerequisite receipt unverified: HOLD")
		}
	}
	return nil
}

func (e *Engine) verifyRecoveryCause(r Run) error {
	cause := r.Recovery
	if cause == nil {
		return errors.New("missing recovery cause")
	}
	plan := r.Plans[cause.Index]
	matches := func(job jobs.Snapshot) error {
		got, _ := json.Marshal(job.Args)
		want, _ := json.Marshal(plan.Args)
		if job.Workdir != r.Root || job.Command != plan.Command || string(got) != string(want) {
			return errors.New("original stage identity mismatch")
		}
		return nil
	}
	if _, err := e.manager.Get(cause.JobID); cause.MissingReceipt && err != nil {
		return e.manager.WithMissingReceipt(r.Root, cause.JobID, cause.Supervisor, cause.LaunchAborted, func(proof *jobs.Snapshot) error {
			if proof != nil {
				return matches(*proof)
			}
			return nil
		})
	}
	return e.manager.WithGoneOwner(r.Root, cause.JobID, func(job jobs.Snapshot) error {
		if err := matches(job); err != nil {
			return err
		}
		if job.State == jobs.Done && job.ExitCode != nil && *job.ExitCode == 0 && !(cause.Stopped && stopped(r)) {
			return errors.New("original stage succeeded")
		}
		return nil
	})
}

func (e *Engine) save(old Run, next Run) (Run, error) {
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(next)
	ok, err := e.db.Replace(runKey(old.ID), a, b)
	if err == nil && !ok {
		err = errors.New("pipeline changed concurrently: reconcile")
	}
	return next, err
}
