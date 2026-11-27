package controlplane

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestControlRouteObservedWithoutClaimingIndependentFailover(t *testing.T) {
	s := testControlService(t)
	s.ip = "/fake/ip"
	s.resolveIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("104.18.1.1")}}, nil
	}
	previousRun := s.run
	s.run = func(ctx context.Context, executable string, args ...string) ([]byte, bool, error) {
		if executable == "/fake/ip" {
			if len(args) != 4 || args[0] != "-4" || args[1] != "route" || args[2] != "get" || args[3] != "104.18.1.1" {
				t.Fatalf("unexpected route command %v", args)
			}
			return []byte("104.18.1.1 via 192.168.0.1 dev eth0 src 192.168.77.10 uid 1000"), false, nil
		}
		return previousRun(ctx, executable, args...)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.ControlRoutes) != 1 {
		t.Fatalf("missing control route: %+v", snap)
	}
	r := snap.ControlRoutes[0]
	if r.Status != "OBSERVED" || r.Interface != "eth0" || r.Gateway != "192.168.0.1" ||
		r.SourceIP != "192.168.77.10" || r.VerifiedIndependence {
		t.Fatalf("observed route promoted to failover proof: %+v", r)
	}
}

func TestControlRouteMissingDNSAndAmbiguousOutputFailClosed(t *testing.T) {
	s := testControlService(t)
	s.ip = "/fake/ip"
	s.resolveIP = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("NXDOMAIN") }
	if r := s.inspectControlRoute(context.Background(), "api.openai.com"); r.Status != "UNKNOWN" ||
		r.VerifiedIndependence || !strings.Contains(r.Reason, "DNS") {
		t.Fatalf("DNS failure: %+v", r)
	}
	for _, line := range []string{"", "unreachable 10.0.0.1", "10.0.0.1 via 10.0.0.2", "10.0.0.1 dev eth0 src bad"} {
		if _, _, _, err := parseRouteGet(line); err == nil {
			t.Errorf("accepted invalid route: %q", line)
		}
	}
}
