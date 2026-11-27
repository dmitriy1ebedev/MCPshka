package releasequeue

import (
	"mcpshka/internal/notify"
	"time"
)

// ProgressOf separates the current observation from the next planned step.
func ProgressOf(q *View, observed string, now time.Time) notify.Progress {
	p := notify.Progress{PlannedStep: "release_apply", ObservedState: observed, Freshness: now, SafeParallelWork: "independent_worktree"}
	if q == nil {
		p.HoldReason = "данные очереди выпуска недоступны"
		return p
	}
	p.PlannedStep = q.Stage
	p.Freshness = q.UpdatedAt
	p.CheckpointRef = "release-queue/" + q.Version + "/" + q.Commit
	p.HoldReason = q.Detail
	switch q.Stage {
	case "DISCOVERED":
		p.PlannedStep = "release_apply action=queue"
	case "QUEUED":
		p.PlannedStep = "integration_ship"
	case "BUILD":
		p.PlannedStep = "release_build_prepare"
	case "STAGE":
		p.PlannedStep = "release_local"
	case "APPLY":
		p.PlannedStep = "release_apply_status"
	case "VERIFY":
		p.PlannedStep = "project_verify"
	case "DONE":
		p.PlannedStep = "acceptance_status"
		p.HoldReason = "выпуск не подтверждает приёмку фазы"
	}
	return p
}
