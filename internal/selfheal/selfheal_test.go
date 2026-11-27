package selfheal

import (
	"context"
	"errors"
	"mcpshka/internal/audit"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/statestore"
	"path/filepath"
	"testing"
	"time"
)

type fakeDocker struct {
	status   dockerops.Status
	policy   string
	calls    int
	err      error
	readErr  error
	names    []string
	detached bool
}

func (d *fakeDocker) RestartContainers() []string { return d.names }
func (d *fakeDocker) Status(context.Context, string) (dockerops.Status, error) {
	return d.status, d.readErr
}
func (d *fakeDocker) Inspect(context.Context, string) (dockerops.Inspect, error) {
	return dockerops.Inspect{ID: "container-id", RestartPolicy: d.policy, Detached: d.detached}, nil
}
func (d *fakeDocker) Restart(context.Context, string) (dockerops.Status, error) {
	d.calls++
	return d.status, d.err
}

type fakeAudit struct {
	events []audit.Event
	fail   bool
}

func (a *fakeAudit) Log(e audit.Event) error {
	if a.fail {
		return errors.New("audit unavailable")
	}
	a.events = append(a.events, e)
	return nil
}

type fixture struct {
	s    *Service
	d    *fakeDocker
	a    *fakeAudit
	now  time.Time
	file string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC), file: filepath.Join(t.TempDir(), "state.db")}
	db, e := statestore.Open(f.file)
	if e != nil {
		t.Fatal(e)
	}
	f.d = &fakeDocker{status: dockerops.Status{Name: "zigbee2mqtt", Status: "running", Running: true, HealthStatus: "unhealthy", StartedAt: "boot1"}, policy: "unless-stopped", names: []string{"zigbee2mqtt"}}
	f.a = &fakeAudit{}
	f.s = New(db, f.d, f.a, Config{true, 5, 3, 2})
	f.s.now = func() time.Time { return f.now }
	return f
}
func (f *fixture) step(t *testing.T) {
	t.Helper()
	if e := f.s.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) advance(t *testing.T, n int) {
	t.Helper()
	for range n {
		f.now = f.now.Add(time.Minute)
		f.step(t)
	}
}
func (f *fixture) state(t *testing.T) Container {
	t.Helper()
	v, e := f.s.Status(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return v.Containers["zigbee2mqtt"]
}
func (f *fixture) reopen(t *testing.T) {
	t.Helper()
	db, e := statestore.Open(f.file)
	if e != nil {
		t.Fatal(e)
	}
	f.s = New(db, f.d, f.a, f.s.config)
	f.s.now = func() time.Time { return f.now }
}
func TestUnhealthyThresholdAndRecovery(t *testing.T) {
	f := setup(t)
	f.step(t)
	f.advance(t, 4)
	if f.d.calls != 0 {
		t.Fatal("early restart")
	}
	f.reopen(t)
	f.advance(t, 1)
	if f.d.calls != 1 {
		t.Fatal("expected one restart")
	}
	f.advance(t, 1)
	if f.d.calls != 1 || f.state(t).Phase != "verifying" {
		t.Fatal("verification repeated repair")
	}
	f.d.status.HealthStatus = "healthy"
	f.advance(t, 1)
	if f.state(t).Phase != "recovered" || f.state(t).History[0].Result != "recovered" {
		t.Fatal("recovery not recorded")
	}
	if len(f.a.events) < 3 {
		t.Fatal("missing audit")
	}
}
func TestFailedVerificationNeverRetriesAcrossRestart(t *testing.T) {
	f := setup(t)
	f.step(t)
	f.advance(t, 5)
	f.reopen(t)
	f.advance(t, 3)
	if f.state(t).Phase != "escalated: recovery not verified" {
		t.Fatal(f.state(t).Phase)
	}
	f.d.status.StartedAt = "external-change"
	f.advance(t, 65)
	f.reopen(t)
	f.advance(t, 1)
	if f.d.calls != 1 {
		t.Fatal("failed repair retried")
	}
}
func TestHourlyLimitPersists(t *testing.T) {
	f := setup(t)
	for range 2 {
		f.d.status.HealthStatus = "unhealthy"
		f.step(t)
		f.advance(t, 5)
		f.d.status.HealthStatus = "healthy"
		f.advance(t, 1)
		f.advance(t, 1)
	}
	if f.d.calls != 2 {
		t.Fatal(f.d.calls)
	}
	f.reopen(t)
	f.d.status.HealthStatus = "unhealthy"
	f.step(t)
	f.advance(t, 5)
	if f.d.calls != 2 || f.state(t).Phase != "escalated: hourly repair limit" {
		t.Fatal(f.state(t))
	}
	f.d.status.HealthStatus = "healthy"
	f.advance(t, 61)
	f.d.status.HealthStatus = "unhealthy"
	f.step(t)
	f.advance(t, 5)
	if f.d.calls != 3 {
		t.Fatal("hourly window did not expire")
	}
}
func TestStoppedAndCrashed(t *testing.T) {
	for _, tc := range []struct {
		code   int
		policy string
		want   int
		err    string
	}{{0, "no", 0, ""}, {143, "no", 0, ""}, {1, "no", 0, ""}, {0, "always", 0, ""}, {1, "", 0, ""}, {1, "unless-stopped", 1, ""},
		// 25.11: Docker's own restarts failed and left exit 0 with a reason.
		{0, "unless-stopped", 1, "failed to create task for container: file exists"}, {0, "no", 0, "failed to create task"}} {
		t.Run(tc.policy+string(rune('0'+tc.code))+tc.err, func(t *testing.T) {
			f := setup(t)
			f.d.status = dockerops.Status{Name: "zigbee2mqtt", Status: "exited", ExitCode: tc.code, Error: tc.err}
			f.d.policy = tc.policy
			f.step(t)
			if f.d.calls != 0 {
				t.Fatal("native restart policy was not given time")
			}
			f.advance(t, 5)
			if f.d.calls != tc.want {
				t.Fatal(f.d.calls)
			}
		})
	}
}
func TestDisabledAllowlistAuditAndDockerFailure(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		f := setup(t)
		f.s.config.Enabled = false
		f.step(t)
		f.advance(t, 6)
		if f.d.calls != 0 {
			t.Fatal("disabled repair")
		}
	})
	t.Run("empty allowlist", func(t *testing.T) {
		f := setup(t)
		f.d.names = nil
		f.step(t)
		f.advance(t, 6)
		if f.d.calls != 0 {
			t.Fatal("unlisted repair")
		}
	})
	t.Run("audit failure", func(t *testing.T) {
		f := setup(t)
		f.a.fail = true
		if f.s.Step(context.Background()) == nil || f.d.calls != 0 {
			t.Fatal("unaudited repair")
		}
	})
	t.Run("restart error", func(t *testing.T) {
		f := setup(t)
		f.d.err = errors.New("lost reply")
		f.step(t)
		f.advance(t, 10)
		f.reopen(t)
		f.advance(t, 1)
		if f.d.calls != 1 || f.state(t).Phase != "escalated: restart failed or outcome unknown" {
			t.Fatal(f.state(t))
		}
	})
}
func TestObservationContinuity(t *testing.T) {
	for _, gap := range []string{"unobserved", "read failure", "healthy"} {
		t.Run(gap, func(t *testing.T) {
			f := setup(t)
			f.step(t)
			f.advance(t, 4)
			switch gap {
			case "unobserved":
				f.now = f.now.Add(4 * time.Minute)
			case "read failure":
				f.d.readErr = errors.New("unavailable")
				f.advance(t, 1)
				f.d.readErr = nil
			case "healthy":
				f.d.status.HealthStatus = "healthy"
				f.advance(t, 1)
				f.d.status.HealthStatus = "unhealthy"
			}
			f.step(t)
			if f.d.calls != 0 {
				t.Fatal("observation gap counted")
			}
			f.advance(t, 5)
			if f.d.calls != 1 {
				t.Fatal("fresh threshold missing")
			}
		})
	}
}
func TestPendingReservationDoesNotReplay(t *testing.T) {
	f := setup(t)
	f.step(t)
	st, raw, e := f.s.read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	c := st.Containers["zigbee2mqtt"]
	c.Phase = "verifying"
	c.Deadline = f.now.Add(3 * time.Minute)
	c.History = []Repair{{f.now, c.Fingerprint, "verifying"}}
	st.Containers["zigbee2mqtt"] = c
	if e = f.s.save(st, raw); e != nil {
		t.Fatal(e)
	}
	f.reopen(t)
	f.advance(t, 4)
	if f.d.calls != 0 || f.state(t).Phase != "escalated: recovery not verified" {
		t.Fatal("reservation replayed")
	}
}
func TestConfigValidation(t *testing.T) {
	t.Setenv("MCPSHKA_SELF_HEAL_ENABLED", "false")
	c, e := FromEnvironment()
	if e != nil || c.Enabled || c.UnhealthyMinutes != 5 || c.MaxRepairs != 2 {
		t.Fatal(c, e)
	}
	t.Setenv("MCPSHKA_SELF_HEAL_MAX_REPAIRS", "0")
	if _, e = FromEnvironment(); e == nil {
		t.Fatal("invalid limit accepted")
	}
}

func TestMalformedRepairStateFailsClosed(t *testing.T) {
	f := setup(t)
	bad := []byte(`{"containers":{"zigbee2mqtt":{"phase":"verifying","history":[]}},"incidents":[]}`)
	if _, e := f.s.db.PutIfAbsent(stateKey, bad); e != nil {
		t.Fatal(e)
	}
	if e := f.s.Step(context.Background()); e == nil || f.d.calls != 0 {
		t.Fatal("malformed state accepted")
	}
}
func TestStaleReservationCannotChangeState(t *testing.T) {
	f := setup(t)
	f.step(t)
	st, raw, e := f.s.read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	f.advance(t, 1)
	if e = f.s.save(st, raw); e == nil {
		t.Fatal("stale state accepted")
	}
	if f.d.calls != 0 {
		t.Fatal("stale reservation repaired")
	}
}
func TestRecoveryRequiresHealthcheckAndCurrentDeadline(t *testing.T) {
	for _, health := range []string{"", "healthy"} {
		t.Run("late-"+health, func(t *testing.T) {
			f := setup(t)
			f.step(t)
			f.advance(t, 5)
			f.d.status.HealthStatus = health
			f.now = f.now.Add(4 * time.Minute)
			f.step(t)
			if f.state(t).Phase != "escalated: recovery not verified" || f.d.calls != 1 {
				t.Fatal(f.state(t))
			}
		})
	}
}

// A running container without a healthcheck that lost its network endpoint
// is restarted at once and recovers once it is attached again.
func TestDetachedNetworkIsRepaired(t *testing.T) {
	f := setup(t)
	f.d.status.HealthStatus = ""
	f.step(t)
	if f.d.calls != 0 {
		t.Fatal("attached container restarted")
	}
	f.d.detached = true
	f.advance(t, 1)
	if f.d.calls != 1 || f.state(t).Phase != "verifying" {
		t.Fatal("detached container not repaired", f.d.calls, f.state(t))
	}
	f.d.detached = false
	f.advance(t, 1)
	if f.state(t).Phase != "recovered" {
		t.Fatal(f.state(t))
	}
}
