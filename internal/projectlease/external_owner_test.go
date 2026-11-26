package projectlease

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

type doneExternal struct{ state jobs.State }

func (doneExternal) Name() string { return "workbench-fake" }
func (doneExternal) Match(c string, _ []string, _ string) bool {
	return c == "make"
}
func (doneExternal) Plan(string, string, []string, string, jobs.StartOptions) (json.RawMessage, error) {
	return json.RawMessage(`{"permit":"p"}`), nil
}
func (doneExternal) Start(json.RawMessage) error { return nil }
func (d *doneExternal) Observe(json.RawMessage, bool) (jobs.ExternalObservation, error) {
	code := 0
	return jobs.ExternalObservation{State: d.state, ExitCode: &code, ResourcesReleased: d.state == jobs.Done}, nil
}

// MCP-265: a workbench workflow has no local PID; once it is done and fenced
// its managed lease is released, and while kept the reason is visible.
func TestManagedLeaseOfExternalOwnerIsReleasedWhenDone(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	p := policy.Default()
	p.AllowedWorkdirs = []string{root}
	m, err := jobs.New(filepath.Join(root, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLeaseGuard(s.Guard)
	rt := &doneExternal{state: jobs.Running}
	m.SetExternalRuntime(rt)
	id := strings.Repeat("c", 32)
	if _, err := s.AcquireManaged(root, id, m); err != nil {
		t.Fatal(err)
	}
	job, err := m.StartWithOptions("make", []string{"test"}, root, jobs.StartOptions{ReservedID: "workflow-" + id})
	if err != nil || job.External == nil {
		t.Fatalf("%+v %v", job, err)
	}
	d := deploy.New(s.policy, filepath.Join(root, "deploys"))
	if r, _ := s.RecoverManaged(root, m, d); r.Released || r.Hold != "owner job active" {
		t.Fatalf("running: %+v", r)
	}
	rt.state = jobs.Done
	s.MaintainManaged(m, d)
	st, err := s.Status(root)
	if err != nil || st.Held {
		t.Fatalf("done external owner kept the lease: %+v %v (hold %q)", st, err, s.RecoveryHold(root))
	}
}
