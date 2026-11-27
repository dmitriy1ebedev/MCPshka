package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/workbenchruntime"
	"mcpshka/internal/workflow"
)

func TestReleaseCleanupNeedsReadEvidenceNotWriterPermission(t *testing.T) {
	manager, leases, in := yieldFixture(t)
	if _, err := leases.Release(in.Path, in.Token); err != nil {
		t.Fatal(err)
	}
	d := &serverDeps{manager: manager, leases: leases, pipelines: workflow.New(manager),
		deploys: deploy.New(manager.Policy(), filepath.Join(filepath.Dir(manager.StateDir()), "deploys")),
		guard:   func(string, string) error { return errors.New("protected writer admission intentionally closed") }}
	if err := d.releaseScopesIdle(context.Background(), in.Path); err != nil {
		t.Fatal("read-only cleanup demanded writer authority", err)
	}
	if _, err := leases.AcquireTracked(in.Path, "other", "fedcba9876543210fedcba9876543210"); err != nil {
		t.Fatal(err)
	}
	if err := d.releaseScopesIdle(context.Background(), in.Path); err == nil {
		t.Fatal("cleanup hid a remaining writer lease")
	}
}

type sealedWorkflowFixtureRuntime struct{}

func (sealedWorkflowFixtureRuntime) Name() string { return "sealed-fixture" }
func (sealedWorkflowFixtureRuntime) Match(command string, _ []string, _ string) bool {
	return command == workbenchruntime.WorkflowCommand
}
func (sealedWorkflowFixtureRuntime) Plan(string, string, []string, string, jobs.StartOptions) (json.RawMessage, error) {
	return nil, errors.New("fixture must stop at admission")
}
func (sealedWorkflowFixtureRuntime) Start(json.RawMessage) error {
	return errors.New("fixture must never dispatch")
}
func (sealedWorkflowFixtureRuntime) Observe(json.RawMessage, bool) (jobs.ExternalObservation, error) {
	return jobs.ExternalObservation{}, errors.New("fixture must never observe")
}
func TestManagedRejectedLaunchReleasesOnlyOwnLease(t *testing.T) {
	manager, leases, in := yieldFixture(t)
	if _, err := leases.Release(in.Path, in.Token); err != nil {
		t.Fatal(err)
	}
	configured := policy.Default()
	configured.AllowedWorkdirs = []string{in.Path}
	*manager.Policy() = *configured
	profile := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "fixture", Match: projectprofile.Match{Markers: []string{"check.py"}}, Workflows: map[string]projectprofile.Workflow{
		"fast": {Command: "python3", Args: []string{"check.py"}, TimeoutSeconds: 60, OutputLimitBytes: 4096},
	}}
	if err := os.MkdirAll(filepath.Join(in.Path, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in.Path, "check.py"), []byte("print(1)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(profile)
	if err := os.WriteFile(filepath.Join(in.Path, ".mcpshka", "profile.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".mcpshka/profile.json", "check.py"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "profile"}} {
		command := exec.Command("git", append([]string{"-C", in.Path}, args...)...)
		if raw, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(raw))
		}
	}
	manager.SetExternalRuntime(sealedWorkflowFixtureRuntime{})
	manager.SetLeaseGuard(func(string, string) error { return errors.New("controlled prelaunch rejection") })
	deployments := deploy.New(manager.Policy(), filepath.Join(filepath.Dir(manager.StateDir()), "deploys"))
	_, err := startManagedWorkflow(manager, leases, deployments, projectprofile.New(manager.Policy()), ProjectRunInput{Path: in.Path, Workflow: "fast", IdempotencyKey: "controlled"})
	if !jobs.IsLaunchRejected(err) {
		t.Fatal("fixture did not reach controlled post-claim rejection", err)
	}
	if state, err := leases.Status(in.Path); err != nil || state.Held {
		t.Fatal("known no-effect launch orphaned its lease", state, err)
	}
}
