package missiontools

import (
	"testing"
	"time"

	"mcpshka/internal/mission"
)

func TestSupersededAgentTaskHistory(t *testing.T) {
	now := time.Date(2026, 10, 24, 1, 30, 0, 0, time.UTC)
	finished := now.Add(-2 * time.Hour)
	quota := mission.Observation{Kind: "agent_task", Phase: "quota"}
	if supersededAgentTask(quota, finished.Add(-time.Hour), finished, now) {
		t.Fatal("open quota without reset or later run was dropped")
	}
	if !supersededAgentTask(quota, finished.Add(time.Minute), finished, now) {
		t.Fatal("quota kept after a later run of the same agent")
	}
	passed := quota
	passed.ResetAt = now.Add(-time.Minute)
	if !supersededAgentTask(passed, time.Time{}, finished, now) {
		t.Fatal("quota kept after its reset")
	}
	failed := mission.Observation{Kind: "agent_task", Phase: "failed"}
	if supersededAgentTask(failed, time.Time{}, finished, now) {
		t.Fatal("recent failure dropped")
	}
	if !supersededAgentTask(failed, time.Time{}, now.Add(-25*time.Hour), now) {
		t.Fatal("day-old failure kept as a blocker")
	}
}
