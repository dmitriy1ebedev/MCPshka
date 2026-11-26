package workflow

import (
	"encoding/json"
	"errors"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/gitops"
	"mcpshka/internal/projectprofile"
)

type missionIntake struct {
	Pin       projectprofile.PipelinePin
	MissionID string
	Deadline  *time.Time
	Snapshot  []byte
	Attempted bool
}

func pinOf(r Run) projectprofile.PipelinePin {
	return projectprofile.PipelinePin{ID: r.ID, Path: r.Root, Pipeline: r.Pipeline, RoadmapSHA: r.RoadmapSHA, Scope: r.Scope, Revision: r.Revision, Fingerprint: r.Fingerprint}
}

func sameDeadline(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// Preview checks existing profile/policy and source without claiming or starting work.
func (e *Engine) Preview(path, name, id, roadmapSHA, scope string) (projectprofile.PipelinePin, error) {
	r, err := e.prepare(path, name, id, roadmapSHA, scope)
	if err != nil {
		return projectprofile.PipelinePin{}, err
	}
	git := gitops.New(e.manager.Policy())
	status, err := git.Status(r.Root, 16384)
	if err != nil || status.Dirty || status.Truncated {
		return projectprofile.PipelinePin{}, errors.New("pipeline preview requires a clean, completely inspected worktree")
	}
	r.Revision, err = git.Revision(r.Root)
	return pinOf(r), err
}

// StartMission receives only the daemon controller's normalized intake payload.
// Parent, child and claim become durable in one write before any stage can run.
func (e *Engine) StartMission(pin projectprofile.PipelinePin, task string, deadline *time.Time, snapshot []byte) (Run, error) {
	var header struct {
		ID         string `json:"id"`
		WorkflowID string `json:"workflow_id"`
		State      string `json:"state"`
	}
	if !validDigest(task, 32) || len(snapshot) > 96<<10 || json.Unmarshal(snapshot, &header) != nil || header.ID != task || header.WorkflowID != pin.ID || header.State != "INTAKE" || deadline != nil && !time.Now().Before(*deadline) {
		return Run{}, failure.Rejected("MISSION_INTAKE_INVALID", "workflow.start_mission", "valid unexpired server-side mission intake required; no work started", nil)
	}
	intake := &missionIntake{Pin: pin, MissionID: task, Deadline: deadline, Snapshot: snapshot}
	r, err := e.start(pin.Path, pin.Pipeline, pin.ID, pin.RoadmapSHA, pin.Scope, intake)
	if err != nil && !intake.Attempted {
		return r, failure.Rejected("MISSION_INTAKE_REFUSED", "workflow.start_mission", "mission preview or admission refused before any effect", err)
	}
	return r, err
}
