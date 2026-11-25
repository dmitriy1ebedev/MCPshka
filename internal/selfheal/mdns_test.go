package selfheal

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcpshka/internal/dockerops"
)

type mdnsDocker struct {
	*fakeDocker
	observation dockerops.LANMDNS
	err         error
}

func (d *mdnsDocker) LANMDNS(context.Context) (dockerops.LANMDNS, error) {
	return d.observation, d.err
}

func mdnsSetup(t *testing.T) (*fixture, *mdnsDocker) {
	t.Helper()
	f := setup(t)
	f.d.names = []string{"go2rtc"}
	f.d.status = dockerops.Status{Name: "go2rtc", Running: true, Status: "running", PID: 123, StartedAt: f.now.Add(-3 * time.Minute).Format(time.RFC3339Nano)}
	d := &mdnsDocker{fakeDocker: f.d, observation: dockerops.LANMDNS{LANIP: "192.168.42.9", PID: 123, StartedAt: f.d.status.StartedAt}}
	f.s.docker = d
	return f, d
}

func TestMDNSDecision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture, *mdnsDocker)
		want   int
	}{
		{"missing LAN socket", func(*fixture, *mdnsDocker) {}, 1},
		{"no LAN address", func(_ *fixture, d *mdnsDocker) { d.observation.LANIP = "" }, 0},
		{"socket present", func(_ *fixture, d *mdnsDocker) { d.observation.Bound = true }, 0},
		{"fresh start", func(f *fixture, d *mdnsDocker) {
			f.d.status.StartedAt = f.now.Add(-119 * time.Second).Format(time.RFC3339Nano)
			d.observation.StartedAt = f.d.status.StartedAt
		}, 0},
		{"two minutes", func(f *fixture, d *mdnsDocker) {
			f.d.status.StartedAt = f.now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
			d.observation.StartedAt = f.d.status.StartedAt
		}, 1},
		{"invalid start", func(f *fixture, d *mdnsDocker) {
			f.d.status.StartedAt, d.observation.StartedAt = "bad", "bad"
		}, 0},
		{"future start", func(f *fixture, d *mdnsDocker) {
			f.d.status.StartedAt = f.now.Add(time.Minute).Format(time.RFC3339Nano)
			d.observation.StartedAt = f.d.status.StartedAt
		}, 0},
		{"stopped", func(f *fixture, _ *mdnsDocker) { f.d.status.Running = false; f.d.status.Status = "exited" }, 0},
		{"native restart", func(f *fixture, _ *mdnsDocker) { f.d.status.Restarting = true }, 0},
		{"observation error", func(_ *fixture, d *mdnsDocker) { d.err = errors.New("permission denied") }, 0},
		{"PID changed", func(_ *fixture, d *mdnsDocker) { d.observation.PID++ }, 0},
		{"start changed", func(_ *fixture, d *mdnsDocker) { d.observation.StartedAt = "other" }, 0},
		{"disabled", func(f *fixture, _ *mdnsDocker) { f.s.config.Enabled = false }, 0},
		{"not allowlisted", func(f *fixture, _ *mdnsDocker) { f.d.names = nil }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, d := mdnsSetup(t)
			tc.change(f, d)
			f.step(t)
			if f.d.calls != tc.want {
				t.Fatalf("restarts=%d, want %d", f.d.calls, tc.want)
			}
			if tc.want == 1 {
				st, err := f.s.Status(context.Background())
				if err != nil || st.Containers["go2rtc"].Failure != mdnsFailure || len(f.a.events) != 3 {
					t.Fatal("missing durable incident/audit", st, err)
				}
			}
		})
	}
}

func TestMDNSCooldownSurvivesControllerRestart(t *testing.T) {
	for _, age := range []time.Duration{14*time.Minute + 59*time.Second, 15 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			f, d := mdnsSetup(t)
			st, raw, err := f.s.read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			st.Containers["go2rtc"] = Container{History: []Repair{{At: f.now.Add(-age), Result: "recovered"}}}
			if err := f.s.save(st, raw); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			f.s.docker = d
			f.step(t)
			want := 0
			if age == 15*time.Minute {
				want = 1
			}
			if f.d.calls != want {
				t.Fatal("cooldown not durable", f.d.calls)
			}
		})
	}
}

func TestMDNSVerificationRequiresSocket(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(map[bool]string{false: "still missing", true: "socket returned"}[recover], func(t *testing.T) {
			f, d := mdnsSetup(t)
			f.step(t)
			f.d.status.HealthStatus = "healthy"
			d.observation.Bound = recover
			f.advance(t, 3)
			st, err := f.s.Status(context.Background())
			want := "escalated: recovery not verified"
			if recover {
				want = "cleared"
				if st.Containers["go2rtc"].Fingerprint != "" {
					t.Fatal("recovered failure not cleared")
				}
			} else if st.Containers["go2rtc"].Phase != want {
				t.Fatal(st, err)
			}
			if f.d.calls != 1 {
				t.Fatal("repair repeated", f.d.calls)
			}
			if !recover {
				f.reopen(t)
				f.s.docker = d
				f.advance(t, 20)
				if f.d.calls != 1 {
					t.Fatal("escalated episode replayed")
				}
			}
		})
	}
}

func TestMDNSUnknownDoesNotClearFailure(t *testing.T) {
	f, d := mdnsSetup(t)
	f.step(t)
	f.advance(t, 3)
	d.observation.LANIP = ""
	f.advance(t, 1)
	st, err := f.s.Status(context.Background())
	if err != nil || st.Containers["go2rtc"].Phase != "escalated: recovery not verified" {
		t.Fatal("unknown cleared failure", st, err)
	}
	d.observation.LANIP, d.observation.Bound = "192.168.42.9", true
	f.advance(t, 1)
	st, _ = f.s.Status(context.Background())
	if st.Containers["go2rtc"].Fingerprint != "" {
		t.Fatal("observed recovery did not clear escalation")
	}
}
