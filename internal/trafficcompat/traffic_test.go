package trafficcompat

import (
	"strings"
	"testing"
)

func TestParseTrafficRouteCompat(t *testing.T) {
	plan, compat, err := Parse("mcpshka", []string{
		"traffic-route", "start",
		"--client", "192.168.77.40",
		"--mac", "02:00:00:00:00:46",
		"--duration", "300",
		"--request-id", "youtube-20261007-1",
	})
	if err != nil || !compat {
		t.Fatalf("valid compat request rejected: compat=%v err=%v", compat, err)
	}
	if plan.Operation != "start" || plan.IdempotencyKey != "compat-traffic-route:youtube-20261007-1" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if got := strings.Join(plan.BrokerArgs, " "); strings.Contains(got, "/tmp") || !strings.Contains(got, "/var/lib/diag/traffic-runs") {
		t.Fatalf("unsafe broker args: %q", got)
	}

	plan, compat, err = Parse("/usr/local/bin/mcpshka", []string{
		"traffic-route", "report", "--run", "20261007T171500Z-1234abcd",
	})
	if err != nil || !compat || plan.Operation != "report" {
		t.Fatalf("report compat request rejected: plan=%+v compat=%v err=%v", plan, compat, err)
	}

	for _, tc := range []struct {
		command string
		args    []string
		compat  bool
	}{
		{"python3", []string{"traffic-route", "start"}, false},
		{"mcpshka", []string{"version"}, false},
		{"mcpshka", []string{"traffic-route", "start", "--client", "192.168.77.40"}, true},
		{"mcpshka", []string{"traffic-route", "report", "--run", "x", "--root", "/tmp"}, true},
	} {
		_, gotCompat, gotErr := Parse(tc.command, tc.args)
		if gotCompat != tc.compat {
			t.Fatalf("%q %q compat=%v want=%v", tc.command, tc.args, gotCompat, tc.compat)
		}
		if tc.compat && gotErr == nil {
			t.Fatalf("%q %q unexpectedly accepted", tc.command, tc.args)
		}
	}
}
