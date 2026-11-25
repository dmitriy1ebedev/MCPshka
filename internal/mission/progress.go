package mission

import (
	"mcpshka/internal/notify"
	"strings"
)

// WithProgress projects observed checkpoints, never a percentage from CI alone.
func WithProgress(r Report) Report {
	r.Progress = notify.Progress{PlannedStep: "mission_status", ObservedState: r.State, Freshness: r.CapturedAt, SafeParallelWork: "UNKNOWN"}
	if r.IndependentWorkAvailable != nil && *r.IndependentWorkAvailable {
		r.SafeParallelWork = "independent_worktree"
	}
	r.HoldReason = strings.Join(r.Incomplete, "; ")
	if len(r.Items) > 0 {
		i := r.Items[0]
		r.PlannedStep = i.NextSafeAction.Tool
		r.HoldReason = strings.Join(i.BlockedBy, "; ")
		r.CheckpointRef = i.Kind + "/" + i.ID
	}
	if s := r.Mission; s != nil {
		r.ObservedState = s.State
		r.PlannedStep = s.NextSafeAction.Tool
		r.Freshness = s.UpdatedAt
		r.CheckpointRef = "mission/" + s.ID
		if s.LastProven != nil {
			r.CheckpointRef += "/" + s.LastProven.Step
		}
		reasons := []string{}
		for _, b := range s.Blockers {
			reasons = append(reasons, b.Reason)
		}
		r.HoldReason = strings.Join(reasons, "; ")
	}
	return r
}
