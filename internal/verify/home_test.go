package verify

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestHomePriorityReportsServiceAndSliceDrift(t *testing.T) {
	drift := ""
	s := &Service{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "inspect" && args[len(args)-1] == "zigbee2mqtt" {
			return []byte(strings.Repeat("a", 64)), nil
		}
		if args[0] == "list-units" {
			return []byte("lane-new.service loaded active running"), nil
		}
		u := args[len(args)-1]
		cpu, io, quota := "1000", "1000", "infinity"
		if u == "user.slice" {
			cpu, io, quota = "30", "30", "3s"
		}
		if u == "mcpshka-heavy.slice" {
			cpu, io, quota = "20", "10", "3s"
		}
		if u == drift {
			io = "900"
		}
		return []byte(fmt.Sprintf("CPUWeight=%s\nIOWeight=%s\nCPUQuotaPerSecUSec=%s", cpu, io, quota)), nil
	}}
	for _, u := range []string{"", "lane-new.service", "mcpshka-heavy.slice", "docker-" + strings.Repeat("a", 64) + ".scope"} {
		drift = u
		result := Result{}
		s.addHomePriority(context.Background(), &result)
		check := findCheck(t, result, "home_priority")
		if check.OK != (u == "") {
			t.Fatalf("%s: %+v", u, check)
		}
		if u != "" && !strings.Contains(check.Detail, u) {
			t.Fatal(check)
		}
	}
}

func TestVerifyHomeDriftFailsOverallResult(t *testing.T) {
	s, repo := testVerifier(t)
	original := s.run
	s.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) == 3 && args[2] == "mcpshka-heavy.slice" {
			return []byte("CPUWeight=20\nIOWeight=10\nCPUQuotaPerSecUSec=4s"), nil
		}
		return original(ctx, name, args...)
	}
	result, err := s.Verify(context.Background(), repo, "0.1.a.25")
	if err != nil || result.OK || findCheck(t, result, "home_priority").OK {
		t.Fatalf("%+v %v", result, err)
	}
}
