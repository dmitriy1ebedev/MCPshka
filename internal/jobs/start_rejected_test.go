package jobs

import (
	"encoding/json"
	"errors"
	"testing"
)

type rejectingPlanRuntime struct{ fakeExternal }

func (r *rejectingPlanRuntime) Plan(string, string, []string, string, StartOptions) (json.RawMessage, error) {
	return nil, errors.New("fixture rejected before any backend")
}
func TestLaunchRejectionProvesNoEffectAndPreservesCause(t *testing.T) {
	m, root, _ := externalManager(t)
	cause := errors.New("controlled admission refusal")
	m.SetLeaseGuard(func(string, string) error { return cause })
	_, err := m.Start("go", []string{"version"}, root)
	if !IsLaunchRejected(err) || !errors.Is(err, cause) {
		t.Fatal("prelaunch proof or cause lost", err)
	}
	if activity := m.ProjectActivityReadOnly(root, 16); activity.Count != 0 {
		t.Fatal("rejection created an active job", activity)
	}
}
func TestLaunchRejectionCoversSealedPlanFailure(t *testing.T) {
	m, root, _ := externalManager(t)
	runtime := &rejectingPlanRuntime{fakeExternal: fakeExternal{name: "fixture"}}
	m.SetExternalRuntime(runtime)
	job, err := m.Start("make", []string{"test"}, root)
	if !IsLaunchRejected(err) || job.ID != "" || runtime.starts != 0 {
		t.Fatal("plan failure became UNKNOWN effect", job, err)
	}
}
