package orchestration

import (
	"context"
	"errors"
	"mcpshka/internal/agents"
	"time"
)

var resumeUnitsEnded = agents.TaskUnitsEnded

func recoverResumeLease(ctx context.Context, t agents.Task, session, owner string, d AgentDeps) error {
	if !resumeUnitsEnded(t.ID) {
		return errors.New("task runner or attempt active or unobservable: HOLD; observe original agent_task_status")
	}
	if d.ReleaseTaskLease != nil {
		d.ReleaseTaskLease(ctx, session, owner, t)
	}
	if d.LeaseHeldBy != nil && d.LeaseHeldBy(t.Path, "agent-task:"+t.ID) {
		return errors.New("task lease remains held: HOLD; reconcile original agent_task lease")
	}
	// A dead runner cannot honor its old quota timer; explicit resume owns it.
	if t.State == "quota" || t.State == "retry_scheduled" {
		t.ResumeAt = time.Time{}
	}
	return agents.CheckResume(ctx, t, d.LeaseHeldBy)
}
