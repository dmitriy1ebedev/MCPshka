package jobs

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPipelineBuildCannotStandInForSealedTests(t *testing.T) {
	now := time.Now()
	m := &Manager{jobs: map[string]*Job{}}
	for _, stage := range []string{"test", "prepare", "build", "lint", "package", "verify"} {
		now = now.Add(time.Second)
		raw, _ := json.Marshal(map[string]string{"root": "/project", "commit": "head", "stage": stage})
		m.jobs[stage] = &Job{ID: stage, Workdir: "/project", State: Done, StartedAt: now, FinishedAt: now.Add(time.Minute), External: &ExternalBinding{Runtime: "fixture", Binding: raw}}
	}
	evidence, ok := m.SealedTestEvidence("/project", "head")
	if !ok || evidence.ID != "test" {
		t.Fatalf("non-test counted as test: %+v", evidence)
	}
	duration, ok := m.LastSealedSuccess("/project")
	if !ok || duration != time.Minute {
		t.Fatal(duration, ok)
	}
	delete(m.jobs, "test")
	if _, ok = m.SealedTestEvidence("/project", "head"); ok {
		t.Fatal("build proves phase test gate")
	}
	if _, ok = m.LastSealedSuccess("/project"); ok {
		t.Fatal("build used as passing test timeout")
	}
}
