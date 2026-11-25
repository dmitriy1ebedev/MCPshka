package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/roadmapscan"
)

func (e *Engine) hold(r Run, reason string) (Run, error) {
	n := r
	n.State = "hold"
	n.Reason = reason
	return e.save(r, n)
}

func (e *Engine) Tick(id string) (Run, error) {
	r, err := e.Get(id)
	if err != nil {
		return r, err
	}
	// A lost or substituted claim never authorizes stage execution after reconnect.
	// HOLD is read-only and terminal runs may already have released their claim.
	if r.State != "hold" && !terminal(r) {
		if err := e.requireWorkflowClaim(r); err != nil {
			return r, err
		}
	}
	if reason, ok := e.rememberedAbort(r); ok && r.State != "hold" {
		if r.LaunchAborted {
			e.forgottenAbort(r)
		} else {
			aborted := r
			aborted.LaunchAborted = true
			aborted.State = "reconciling"
			aborted.Reason = reason
			next, err := e.save(r, aborted)
			if err == nil {
				e.forgottenAbort(r)
			}
			return next, err
		}
	}
	if r.State == "hold" {
		return r, nil
	}
	if terminal(r) {
		return r, e.finish(r)
	}
	if r.Deadline != nil && !time.Now().Before(*r.Deadline) && !stopped(r) {
		next := r
		next.Control = "expired"
		return e.save(r, next)
	}
	if err := e.verifyPrerequisites(r); err != nil {
		return r, err
	}
	if r.Index == 10 && !r.LaunchClaimed {
		if err := e.verifyRecoveryCause(r); err != nil {
			return e.hold(r, "recovery cause unavailable: "+err.Error())
		}
	}
	if r.LaunchClaimed {
		job, err := e.manager.Get(r.JobID)
		if err != nil {
			if !r.LaunchAborted && !r.LaunchAt.IsZero() && time.Since(r.LaunchAt) < 30*time.Second {
				return r, nil
			}
			if r.LaunchProtocol == jobs.ManagedLaunchProtocol {
				return e.recoverMissingReceipt(r)
			}
			return e.hold(r, "launch receipt missing; reconcile before any retry")
		}
		plan := r.Plans[r.Index]
		if job.Command != plan.Command || job.Workdir != r.Root {
			return e.hold(r, "job identity mismatch")
		}
		a, _ := json.Marshal(job.Args)
		b, _ := json.Marshal(plan.Args)
		if string(a) != string(b) {
			return e.hold(r, "job arguments mismatch")
		}
		if job.State == jobs.Queued || job.State == jobs.Running || job.State == jobs.Cancelling {
			return r, nil
		}
		if job.State == jobs.Unknown {
			job, err = e.manager.Reconcile(r.JobID)
			if err != nil || !job.ResourcesReleased {
				n := r
				n.State = "reconciling"
				n.Reason = "unknown job: waiting for verified resource disappearance"
				if r.State == n.State && r.Reason == n.Reason {
					return r, nil
				}
				return e.save(r, n)
			}
		}
		if job.Process != nil && job.Process.Alive {
			return e.hold(r, "job outcome/resources unknown")
		}
		if err := e.manager.WithGoneOwner(r.Root, r.JobID, func(jobs.Snapshot) error { return nil }); err != nil {
			return e.hold(r, "stage resources remain: "+err.Error())
		}
		n := r
		n.LaunchClaimed = false
		n.JobID = ""
		success := job.State == jobs.Done && job.ExitCode != nil && *job.ExitCode == 0
		switch {
		case r.Index < 10 && stopped(r):
			n.Recovery = &RecoveryCause{Index: r.Index, JobID: r.JobID, Supervisor: r.Supervisor, LaunchAborted: r.LaunchAborted, Stopped: success}
			n.Index = 10
			n.State = "rollback"
		case !success && r.Index >= 10:
			return e.hold(r, "recovery workflow failed")
		case !success:
			n.Recovery = &RecoveryCause{Index: r.Index, JobID: r.JobID, Supervisor: r.Supervisor, LaunchAborted: r.LaunchAborted}
			n.Index = 10
			n.State = "rollback"
		case r.Index == 10:
			n.Index = 11
			n.State = "finalizing"
		case r.Index == 11:
			n.State = "rolled_back"
		case r.Index == 9:
			n.State = "done"
		default:
			n.Index++
			n.State = "pending"
		}
		next, err := e.save(r, n)
		if err != nil {
			return next, err
		}
		if n.State == "done" || n.State == "rolled_back" {
			// Never drop the claim while another admitted process remains.
			err = e.finish(n)
		}
		return next, err
	}
	if r.Control == "paused" {
		return r, nil
	}
	if stopped(r) && r.Index < 10 {
		return e.stopBeforeDispatch(r)
	}
	root, plans, err := e.plans(r.Root, r.Pipeline)
	if err != nil {
		return e.hold(r, "profile/policy changed: "+err.Error())
	}
	pending, err := e.deploys.Pending()
	if err != nil {
		return e.hold(r, "deployment authority unavailable")
	}
	if pending {
		return r, nil
	}
	raw, _ := json.Marshal(plans)
	if root != r.Root || planDigest(raw, r.Schema) != r.Fingerprint {
		return e.hold(r, "profile plan changed")
	}
	if _, err := roadmapscan.Load(filepath.Join(r.Root, "ROADMAP.md"), r.RoadmapSHA); err != nil {
		return e.hold(r, "roadmap changed: "+err.Error())
	}
	if r.Index == 0 {
		rev, err := gitops.New(e.manager.Policy()).Revision(r.Root)
		if err != nil || rev != r.Revision {
			return e.hold(r, "starting Git revision changed")
		}
	}
	plan := r.Plans[r.Index]
	n := r
	n.LaunchClaimed = true
	n.LaunchAt = time.Now().UTC()
	if r.Index < 10 && r.Deadline != nil && !n.LaunchAt.Before(*r.Deadline) {
		next := r
		next.Control = "expired"
		return e.save(r, next)
	}
	n.LaunchProtocol = jobs.ManagedLaunchProtocol
	n.LaunchAborted = false
	n.Supervisor, err = jobs.CurrentOwnerIdentity()
	if err != nil {
		return e.hold(r, "launch supervisor cannot be bound")
	}
	n.JobID = stageJobID(r.ID, r.Index)
	n.State = "running"
	if r.Index >= 10 {
		n.State = r.State
	}
	n, err = e.save(r, n)
	if err != nil {
		return n, err
	}
	// A pipe gate cannot execute until immutable owner proof is committed.
	_, err = e.manager.StartWithOptions(plan.Command, plan.Args, r.Root, jobs.StartOptions{ReservedID: n.JobID, PrivateTemp: true, Timeout: time.Duration(min(plan.TimeoutSeconds, 300)) * time.Second, OutputLimitBytes: min(plan.OutputLimitBytes, 16384), MemoryLimitBytes: 512 * 1024 * 1024, CPUQuotaPercent: 50, CPUTimeLimit: 300 * time.Second, ProcessLimit: 32, Env: map[string]string{"GOMAXPROCS": "1", "GOMEMLIMIT": "384MiB"}})
	if err != nil {
		aborted := n
		aborted.LaunchAborted = true
		aborted.State = "reconciling"
		aborted.Reason = "launch returned without acceptance: " + err.Error()
		e.rememberAbort(n, aborted.Reason)
		saved, saveErr := e.save(n, aborted)
		if saveErr == nil {
			e.forgottenAbort(n)
		}
		return saved, saveErr
	}
	return n, nil
}

func (e *Engine) recoverMissingReceipt(r Run) (Run, error) {
	n := r
	err := e.manager.WithMissingReceipt(r.Root, r.JobID, r.Supervisor, r.LaunchAborted, func(proof *jobs.Snapshot) error {
		if proof != nil {
			plan := r.Plans[r.Index]
			a, _ := json.Marshal(proof.Args)
			b, _ := json.Marshal(plan.Args)
			if proof.Command != plan.Command || string(a) != string(b) {
				return errors.New("launch proof differs from pinned plan")
			}
		}
		if r.Index >= 10 {
			return errors.New("recovery outcome missing; HOLD")
		}
		n.Recovery = &RecoveryCause{Index: r.Index, JobID: r.JobID, Supervisor: r.Supervisor, LaunchAborted: r.LaunchAborted, MissingReceipt: true}
		n.LaunchClaimed = false
		n.JobID = ""
		n.LaunchAborted = false
		n.Index = 10
		n.State = "rollback"
		n.Reason = "missing exit: original stage never replayed; verified compensation"
		var err error
		n, err = e.save(r, n)
		return err
	})
	if err == nil {
		return n, nil
	}
	if r.Index >= 10 {
		return e.hold(r, err.Error())
	}
	n = r
	n.State = "reconciling"
	n.Reason = "missing receipt: " + err.Error()
	if n.State == r.State && n.Reason == r.Reason {
		return r, nil
	}
	return e.save(r, n)
}

func (e *Engine) release(r Run) error {
	if _, err := e.db.Get(claimKey(r.Root)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if r.State == "cancelled" {
		return e.releaseUnstarted(r)
	}
	if err := e.verifyPrerequisites(r); err != nil {
		return err
	}
	id := stageJobID(r.ID, r.Index)
	return e.manager.WithGoneOwner(r.Root, id, func(job jobs.Snapshot) error {
		plan := r.Plans[r.Index]
		gotArgs, _ := json.Marshal(job.Args)
		wantArgs, _ := json.Marshal(plan.Args)
		if job.State != jobs.Done || job.ExitCode == nil || *job.ExitCode != 0 ||
			job.Workdir != r.Root || job.Command != plan.Command || string(gotArgs) != string(wantArgs) {
			return errors.New("terminal stage receipt unverified: HOLD")
		}
		owner, err := e.db.Get(claimKey(r.Root))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if string(owner) != r.ID {
			return errors.New("pipeline claim changed")
		}
		ok, err := e.db.Delete(claimKey(r.Root), owner)
		if err == nil && !ok {
			err = errors.New("claim changed concurrently")
		}
		return err
	})
}
