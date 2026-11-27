package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/healthlayer"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/systemdsvc"
)

func TestHealthBrokerVersionIsControllerVersion(t *testing.T) {
	if rootbroker.BrokerVersion != Version {
		t.Fatalf("broker %s != controller %s", rootbroker.BrokerVersion, Version)
	}
	raw, err := os.ReadFile("../../VERSION")
	if os.IsNotExist(err) {
		// Older sealed module snapshots omit the root version file.
		if _, gitErr := os.Stat("../../.git"); os.IsNotExist(gitErr) {
			return
		}
	}
	if err != nil || strings.TrimSpace(string(raw)) != Version {
		t.Fatalf("VERSION file does not match controller version %s: %q %v", Version, raw, err)
	}
}

func TestHealthBlockerProbes(t *testing.T) {
	for _, tc := range []struct {
		version string
		fail    bool
		want    string
	}{{Version, false, "ok"}, {"0.0.0", false, "degraded"}, {"", false, "unknown"}, {Version, true, "unknown"}} {
		run := func(ctx context.Context, family string, args ...string) ([]byte, error) {
			if family != "health" || len(args) != 1 {
				t.Fatal("unbounded broker call")
			}
			if tc.fail {
				return nil, errors.New("broker unavailable")
			}
			if args[0] == "broker" {
				return json.Marshal(map[string]any{"version": tc.version, "captured_at": time.Now().UTC()})
			}
			return []byte(`{"permits":{"state":"ok"},"units":{"state":"ok"}}`), nil
		}
		r, err := healthlayer.New(blockerHealthProbes(run)).Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.Items[0].Name != "broker" || r.Items[0].State != tc.want {
			t.Fatalf("broker: %+v", r.Items[0])
		}
		for _, item := range r.Items {
			if item.State != "ok" && item.NextSafeAction == nil {
				t.Fatalf("missing safe action: %+v", item)
			}
		}
	}
	for _, raw := range []string{`{}`, `{"permits":{"state":"ok"}}`, `{"permits":{"state":"unknown"},"units":{"state":"ok"}}`, `{"permits":{"state":"degraded"},"units":{"state":"ok"}}`} {
		run := func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil }
		r, err := healthlayer.New(blockerHealthProbes(run)).Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.Items[1].State == "ok" {
			t.Fatalf("missing or degraded executor evidence green: %s", raw)
		}
	}
}

func TestHealthLaneAndIndexCannotTakeTunnelOrDockerOffline(t *testing.T) {
	read := func(_ context.Context, name string) (systemdsvc.Status, error) {
		s := systemdsvc.Status{ActiveState: "active"}
		if name == "laned.service" {
			s.ActiveState = "inactive"
			s.UnitFileState = "disabled"
		}
		return s, nil
	}
	sample, err := criticalServicesHealth(context.Background(), read)
	if err != nil || sample.State != "ok" {
		t.Fatalf("disabled laned failed: %+v %v", sample, err)
	}
	var services map[string]string
	json.Unmarshal(sample.Data, &services)
	if services["laned.service"] != "disabled" || services["docker.service"] != "active" || services["mcpshka-tunnel.service"] != "active" {
		t.Fatal(services)
	}
	probes := []healthlayer.Probe{{Name: "critical_services", Read: func(context.Context) (healthlayer.Sample, error) { return sample, nil }}, {Name: "lane_network", Read: func(context.Context) (healthlayer.Sample, error) { return healthlayer.Sample{State: "degraded"}, nil }}, {Name: "search_index", Read: func(context.Context) (healthlayer.Sample, error) { return healthlayer.Sample{State: "stale"}, nil }}}
	r, err := healthlayer.New(probes).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "degraded" || r.Items[0].State != "ok" || r.Items[1].State != "degraded" || r.Items[2].State != "stale" {
		t.Fatal(fmt.Sprint(r))
	}
}

func TestHealthExecutorMissingAndStaleEvidence(t *testing.T) {
	for _, tc := range []struct {
		permits, units string
		at             time.Time
		want           string
	}{
		{"ok", "ok", time.Now().UTC(), "ok"},
		{"ok", "degraded", time.Now().UTC(), "degraded"},
		{"unknown", "ok", time.Now().UTC(), "unknown"},
		{"ok", "ok", time.Time{}, "unknown"},
		{"ok", "ok", time.Now().Add(-time.Hour), "stale"},
		{"ok", "OK", time.Now().UTC(), "unknown"},
	} {
		run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "broker" {
				return json.Marshal(map[string]any{"version": Version, "captured_at": time.Now().UTC()})
			}
			return json.Marshal(map[string]any{"permits": map[string]any{"state": tc.permits, "captured_at": tc.at}, "units": map[string]any{"state": tc.units}})
		}
		r, err := healthlayer.New(blockerHealthProbes(run)).Read(context.Background())
		if err != nil || r.Items[1].State != tc.want {
			t.Fatalf("executor %s/%s: %+v %v", tc.permits, tc.units, r, err)
		}
	}
}

func TestHealthDisabledCriticalUnitsAreNotHealthy(t *testing.T) {
	for _, disabled := range []string{"mcpshka.service", "mcpshka-tunnel.service", "docker.service", "laned.service"} {
		sample, err := criticalServicesHealth(context.Background(), func(_ context.Context, name string) (systemdsvc.Status, error) {
			s := systemdsvc.Status{ActiveState: "active"}
			if name == disabled {
				s.ActiveState = "inactive"
				s.UnitFileState = "disabled"
			}
			return s, nil
		})
		want := "degraded"
		if disabled == "laned.service" {
			want = "ok"
		}
		if err != nil || sample.State != want {
			t.Fatalf("disabled %s: %s %v", disabled, sample.State, err)
		}
	}
}
