package runtimehealth

import (
	"testing"

	"mcpshka/internal/roadmapscan"
)

func TestRoadmapWarningDoesNotBlockWorkflow(t *testing.T) {
	warn := roadmapscan.Room{Bytes: 251422, Limit: roadmapscan.MaxBytes, Percent: 95, Warn: true}
	if roadmapBlocksWorkflow(warn) {
		t.Fatal("95% headroom warning degraded jobs_workflow")
	}
	if !roadmapBlocksWorkflow(roadmapscan.Room{Bytes: roadmapscan.MaxBytes + 1, Limit: roadmapscan.MaxBytes}) {
		t.Fatal("unloadable roadmap must degrade jobs_workflow")
	}
}
