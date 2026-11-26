package projectprofile

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"mcpshka/internal/jobs"
	"mcpshka/internal/statestore"
)

// Checkpoint is a compare-and-swap workflow step journal entry. A checkpoint is
// evidence of a step transition, not permission to run an arbitrary command.
type Checkpoint struct {
	WorkflowID             string `json:"workflow_id"`
	PlanFingerprint        string `json:"plan_fingerprint"`
	Step                   string `json:"step"`
	Status                 string `json:"status"`
	Revision               uint64 `json:"revision"`
	ReconciliationEvidence string `json:"reconciliation_evidence,omitempty"`
	JobID                  string `json:"job_id,omitempty"`
}

func checkpointKey(id string) (string, error) {
	if !validToken(id) {
		return "", errors.New("invalid workflow identity")
	}
	return "workflows/v1/" + id, nil
}

// StartCheckpoint creates one durable workflow identity and pins its plan.
// Duplicate starts never overwrite an existing workflow.
func StartCheckpoint(db *statestore.DB, id, fingerprint string) (Checkpoint, bool, error) {
	key, err := checkpointKey(id)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if len(fingerprint) != 64 {
		return Checkpoint{}, false, errors.New("invalid plan fingerprint")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return Checkpoint{}, false, errors.New("invalid plan fingerprint")
	}
	c := Checkpoint{WorkflowID: id, PlanFingerprint: fingerprint, Step: "inspect", Status: "pending", Revision: 1}
	raw, err := json.Marshal(c)
	if err != nil {
		return Checkpoint{}, false, err
	}
	created, err := db.PutIfAbsent(key, raw)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if !created {
		current, err := LoadCheckpoint(db, id)
		if err != nil {
			return Checkpoint{}, false, err
		}
		if current.PlanFingerprint != fingerprint {
			return Checkpoint{}, false, errors.New("workflow identity conflicts with pinned plan")
		}
		return current, false, nil
	}
	return c, true, nil
}

func LoadCheckpoint(db *statestore.DB, id string) (Checkpoint, error) {
	key, err := checkpointKey(id)
	if err != nil {
		return Checkpoint{}, err
	}
	raw, err := db.Get(key)
	if err != nil {
		return Checkpoint{}, err
	}
	var c Checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return Checkpoint{}, err
	}
	if c.WorkflowID != id || c.Revision == 0 || len(c.PlanFingerprint) != 64 {
		return Checkpoint{}, errors.New("invalid durable workflow checkpoint")
	}
	return c, nil
}

var workflowSteps = []string{"inspect", "modify", "test", "review", "commit", "build", "deploy", "reconnect", "verify", "cleanup"}

func nextWorkflowStep(current, next string) bool {
	for i := 0; i+1 < len(workflowSteps); i++ {
		if workflowSteps[i] == current {
			return workflowSteps[i+1] == next
		}
	}
	return false
}

// AdvanceCheckpoint refuses stale writers, plan drift and invalid transitions.
// Recovery from UNKNOWN must be reconciled before a later step is claimed.
func AdvanceCheckpoint(db *statestore.DB, current Checkpoint, step, status string) (Checkpoint, bool, error) {
	key, err := checkpointKey(current.WorkflowID)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if !validToken(step) || !validToken(status) {
		return Checkpoint{}, false, errors.New("invalid step transition")
	}
	if status != "pending" && status != "running" && status != "done" && status != "failed" && status != "unknown" && status != "hold" {
		return Checkpoint{}, false, errors.New("unsupported workflow status")
	}
	raw, err := db.Get(key)
	if err != nil {
		return Checkpoint{}, false, err
	}
	var actual Checkpoint
	if err := json.Unmarshal(raw, &actual); err != nil {
		return Checkpoint{}, false, err
	}
	if actual != current || current.Revision == ^uint64(0) {
		return Checkpoint{}, false, errors.New("stale workflow checkpoint or plan mismatch")
	}
	if current.Status == "unknown" || current.Status == "hold" || current.Status == "failed" {
		return Checkpoint{}, false, errors.New("workflow requires explicit reconciliation")
	}
	if step != current.Step && (current.Status != "done" || status != "pending" || !nextWorkflowStep(current.Step, step)) {
		return Checkpoint{}, false, errors.New("step must complete before advancing")
	}
	if step == current.Step {
		allowed := (current.Status == "pending" && (status == "running" || status == "hold")) ||
			(current.Status == "running" && (status == "done" || status == "failed" || status == "unknown" || status == "hold"))
		if !allowed {
			return Checkpoint{}, false, errors.New("invalid same-step status transition")
		}
	}
	next := current
	next.Step, next.Status, next.Revision = step, status, current.Revision+1
	if step != current.Step {
		next.JobID = ""
		next.ReconciliationEvidence = ""
	}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return Checkpoint{}, false, err
	}
	ok, err := db.Replace(key, raw, nextRaw)
	return next, ok, err
}

// ReconcileCheckpoint records an explicit, evidence-backed outcome after an
// interrupted step. It never re-executes the step and cannot advance the stage.
func ReconcileCheckpoint(db *statestore.DB, current Checkpoint, evidence, outcome string) (Checkpoint, bool, error) {
	if len(evidence) < 8 || len(evidence) > 512 || strings.ContainsAny(evidence, "\x00\r\n") {
		return Checkpoint{}, false, errors.New("bounded reconciliation evidence required")
	}
	if current.Status != "unknown" && current.Status != "hold" && current.Status != "failed" {
		return Checkpoint{}, false, errors.New("only blocked workflow steps can be reconciled")
	}
	if outcome != "done" && outcome != "hold" {
		return Checkpoint{}, false, errors.New("reconciliation outcome must be done or hold")
	}
	key, err := checkpointKey(current.WorkflowID)
	if err != nil {
		return Checkpoint{}, false, err
	}
	raw, err := db.Get(key)
	if err != nil {
		return Checkpoint{}, false, err
	}
	var actual Checkpoint
	if err := json.Unmarshal(raw, &actual); err != nil {
		return Checkpoint{}, false, err
	}
	if actual != current || current.Revision == ^uint64(0) {
		return Checkpoint{}, false, errors.New("stale reconciliation or plan mismatch")
	}
	next := current
	next.Status = outcome
	next.ReconciliationEvidence = evidence
	next.Revision++
	encoded, err := json.Marshal(next)
	if err != nil {
		return Checkpoint{}, false, err
	}
	ok, err := db.Replace(key, raw, encoded)
	return next, ok, err
}

// BindCheckpointJob atomically binds a launched job to its running stage.
func BindCheckpointJob(db *statestore.DB, current Checkpoint, jobID string) (Checkpoint, bool, error) {
	if len(jobID) < 3 || len(jobID) > 96 || strings.ContainsAny(jobID, "/\\\x00\r\n") {
		return Checkpoint{}, false, errors.New("invalid job identity")
	}
	if current.Status != "running" || current.JobID != "" {
		return Checkpoint{}, false, errors.New("stage is not available for job binding")
	}
	key, err := checkpointKey(current.WorkflowID)
	if err != nil {
		return Checkpoint{}, false, err
	}
	raw, err := db.Get(key)
	if err != nil {
		return Checkpoint{}, false, err
	}
	var actual Checkpoint
	if err := json.Unmarshal(raw, &actual); err != nil {
		return Checkpoint{}, false, err
	}
	if actual != current || current.Revision == ^uint64(0) {
		return Checkpoint{}, false, errors.New("stale job association")
	}
	next := current
	next.JobID = jobID
	next.Revision++
	encoded, err := json.Marshal(next)
	if err != nil {
		return Checkpoint{}, false, err
	}
	ok, err := db.Replace(key, raw, encoded)
	return next, ok, err
}

// ReconcileCheckpointJob verifies the recorded bounded job's actual terminal
// receipt before advancing. UNKNOWN or missing jobs are never treated as success.
func ReconcileCheckpointJob(db *statestore.DB, current Checkpoint, manager *jobs.Manager) (Checkpoint, bool, error) {
	if manager == nil || current.JobID == "" {
		return Checkpoint{}, false, errors.New("recorded job and manager required")
	}
	if current.Status != "running" && current.Status != "unknown" {
		return Checkpoint{}, false, errors.New("job stage not eligible for receipt reconciliation")
	}
	job, err := manager.Get(current.JobID)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if job.State == jobs.Unknown || job.State == jobs.Queued || job.State == jobs.Running || job.State == jobs.Cancelling {
		return Checkpoint{}, false, errors.New("job not terminal: HOLD")
	}
	if job.State != jobs.Done || job.ExitCode == nil || *job.ExitCode != 0 {
		if current.Status == "unknown" {
			return ReconcileCheckpoint(db, current, "verified failed job receipt "+job.ID, "hold")
		}
		return AdvanceCheckpoint(db, current, current.Step, "hold")
	}
	if current.Status == "unknown" {
		return ReconcileCheckpoint(db, current, "verified bounded job receipt "+job.ID, "done")
	}
	return AdvanceCheckpoint(db, current, current.Step, "done")
}

// ReconcilePlannedJob refuses to accept a successful receipt from an unrelated
// command, worktree, or workflow plan. The checkpoint and plan are pinned.
func ReconcilePlannedJob(db *statestore.DB, current Checkpoint, plan Plan, manager *jobs.Manager) (Checkpoint, bool, error) {
	if manager == nil || current.PlanFingerprint != plan.Fingerprint || current.JobID == "" {
		return Checkpoint{}, false, errors.New("workflow plan or job identity mismatch: HOLD")
	}
	job, err := manager.Get(current.JobID)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if job.Command != plan.Command || filepath.Clean(job.Workdir) != filepath.Clean(plan.Root) ||
		len(job.Args) != len(plan.Args) {
		return Checkpoint{}, false, errors.New("job receipt does not match pinned execution plan: HOLD")
	}
	for i := range job.Args {
		if job.Args[i] != plan.Args[i] {
			return Checkpoint{}, false, errors.New("job arguments do not match pinned plan: HOLD")
		}
	}
	return ReconcileCheckpointJob(db, current, manager)
}

// ResumeCheckpoint is a fail-closed restart decision: an in-flight step
// becomes UNKNOWN and requires independent job/deploy evidence before retry.
func ResumeCheckpoint(db *statestore.DB, id, expectedFingerprint string) (Checkpoint, bool, error) {
	current, err := LoadCheckpoint(db, id)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if current.PlanFingerprint != expectedFingerprint {
		return Checkpoint{}, false, errors.New("pinned workflow plan changed; HOLD")
	}
	if current.Status != "running" {
		return current, false, nil
	}
	return AdvanceCheckpoint(db, current, current.Step, "unknown")
}

// FinishCheckpoint marks a completed cleanup as the only terminal success.
// No caller can skip any intermediate workflow stage.
func FinishCheckpoint(db *statestore.DB, id, expectedFingerprint string) (Checkpoint, error) {
	current, err := LoadCheckpoint(db, id)
	if err != nil {
		return Checkpoint{}, err
	}
	if current.PlanFingerprint != expectedFingerprint {
		return Checkpoint{}, errors.New("pinned workflow plan changed; HOLD")
	}
	if current.Step != "cleanup" || current.Status != "done" {
		return Checkpoint{}, errors.New("workflow cannot finish before verified cleanup")
	}
	return current, nil
}
