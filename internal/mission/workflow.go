package mission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mcpshka/internal/failure"
	"os"
	"regexp"
	"time"
)

var ErrAcceptancePending = errors.New("HOLD: mission acceptance not proven; retain original workflow completion")

var workflowID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type WorkflowEvidence struct {
	MissionID   string `json:"mission_id,omitempty"`
	Control     string `json:"control,omitempty"`
	ID          string `json:"id"`
	State       string `json:"state"`
	Fingerprint string `json:"step_fingerprint"`
	JobID       string `json:"job_id,omitempty"`
	Index       int    `json:"index"`
}

func (c *Controller) workflowEvidence(ctx context.Context, p Plan) (*WorkflowEvidence, error) {
	var wire struct {
		ID, Root, Scope, State, Fingerprint string
		MissionID                           string `json:"mission_id"`
		Control                             string `json:"control"`
		JobID                               string `json:"job_id"`
		Index                               int
		Revision                            string `json:"revision"`
		RoadmapSHA                          string `json:"roadmap_sha256"`
		Pipeline                            string `json:"pipeline"`
	}
	if err := c.read(ctx, "workflow_status", map[string]any{"id": p.WorkflowID}, &wire); err != nil {
		return nil, err
	}
	fp, err := hex.DecodeString(wire.Fingerprint)
	if err != nil || len(fp) != 32 || wire.ID != p.WorkflowID || wire.Root != p.Path || wire.Scope != p.Steps[0].ID || wire.Index < 0 || wire.Index > 11 || wire.MissionID != "" && wire.MissionID != workflowMissionID(p.WorkflowID) {
		return nil, errors.New("workflow identity/scope/fingerprint mismatch: HOLD")
	}
	if pin := p.WorkflowPlan; pin != nil && (wire.Revision != pin.Revision || wire.RoadmapSHA != pin.RoadmapSHA || wire.Fingerprint != pin.Fingerprint || wire.Pipeline != pin.Pipeline) {
		return nil, errors.New("original workflow differs from mission preview: HOLD")
	}
	out := &WorkflowEvidence{ID: wire.ID, State: wire.State, Fingerprint: wire.Fingerprint, JobID: wire.JobID, Index: wire.Index, MissionID: wire.MissionID, Control: wire.Control}
	switch wire.State {
	case "pending", "running", "rollback", "finalizing", "done", "cancelled":
		return out, nil
	default:
		return out, errors.New("workflow outcome requires reconciliation: HOLD")
	}
}
func workflowMissionID(id string) string {
	sum := sha256.Sum256([]byte("workflow-mission/v1:" + id))
	return hex.EncodeToString(sum[:16])
}

// WorkflowCompleted is called after the existing executor releases its writer
// claim. It only revalidates acceptance/checkpoints; it never creates work.
func (c *Controller) WorkflowCompleted(id string) error {
	if c.db == nil || !workflowID.MatchString(id) {
		return errors.New("invalid workflow completion")
	}
	s, old, err := c.load(workflowMissionID(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.WorkflowID != id {
		return errors.New("mission binding changed: HOLD")
	}
	if s.State == "DONE" || s.State == "PAUSED" || s.State == "CANCELLED" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s.State = "READY"
	if err = c.refresh(ctx, &s, true); err != nil {
		return err
	}
	_, err = c.save(s, old, "workflow_completed", "post-step workflow completion")
	if err != nil {
		return err
	}
	if s.State == "CANCELLED" || s.State == "EXPIRED" {
		if s.Workflow != nil && (s.Workflow.State == "cancelled" || s.Workflow.State == "rolled_back") {
			return nil
		}
	}
	if s.State != "DONE" {
		return ErrAcceptancePending
	}
	return nil
}

// Control is an effect boundary: validate the original scope and pinned plan
// before the daemon's trusted engine hook can bind or alter anything.
func (c *Controller) controlWorkflow(ctx context.Context, s Snapshot, mode string) error {
	if s.WorkflowID == "" || s.Mode == "AUDIT_ONLY" {
		return nil
	}
	if c.WorkflowControl == nil {
		return failure.Rejected("WORKFLOW_CONTROL_UNAVAILABLE", "mission.control", "configured workflow control is unavailable; nothing was changed", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	observed, err := c.workflowEvidence(ctx, s.Plan)
	if observed == nil {
		return failure.RejectedAs(failure.EvidenceMissing, "WORKFLOW_CONTROL_UNVERIFIED", "mission.control", "cannot verify the original workflow; no control was applied", err)
	}
	if observed.MissionID != "" && observed.MissionID != s.ID || s.Workflow != nil && observed.Fingerprint != s.Workflow.Fingerprint {
		return failure.RejectedAs(failure.PolicyDenied, "WORKFLOW_CONTROL_PIN_MISMATCH", "mission.control", "original workflow scope or fingerprint changed; no control was applied", nil)
	}
	return c.WorkflowControl(s.WorkflowID, s.ID, mode, s.Deadline)
}
