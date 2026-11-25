package controlplane

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// OutboundRoute is a read-only routing observation for an actual resolved
// control destination. It is NOT proof of independence under VPN failure:
// that requires a live fault-injection acceptance test on a staging gateway.
type OutboundRoute struct {
	Destination          string    `json:"destination"`
	ResolvedIP           string    `json:"resolved_ip,omitempty"`
	Interface            string    `json:"interface,omitempty"`
	SourceIP             string    `json:"source_ip,omitempty"`
	Gateway              string    `json:"gateway,omitempty"`
	Status               string    `json:"status"`
	Reason               string    `json:"reason,omitempty"`
	VerifiedIndependence bool      `json:"verified_independence"`
	ObservedAt           time.Time `json:"observed_at"`
}

func parseRouteGet(raw string) (dev, source, via string, err error) {
	words := strings.Fields(raw)
	if len(words) == 0 {
		return "", "", "", fmt.Errorf("empty route")
	}
	// Route get may return "RTNETLINK answers..." or an "unreachable" route.
	if words[0] == "unreachable" || strings.HasPrefix(words[0], "RTNETLINK") {
		return "", "", "", fmt.Errorf("route unavailable")
	}
	for i := 0; i+1 < len(words); i++ {
		switch words[i] {
		case "dev":
			dev = words[i+1]
		case "src":
			source = words[i+1]
		case "via":
			via = words[i+1]
		}
	}
	if dev == "" {
		return "", "", "", fmt.Errorf("route has no outgoing interface")
	}
	if source != "" && net.ParseIP(source) == nil {
		return "", "", "", fmt.Errorf("invalid route source")
	}
	if via != "" && net.ParseIP(via) == nil {
		return "", "", "", fmt.Errorf("invalid route gateway")
	}
	return dev, source, via, nil
}

// inspectControlRoute never edits LANE rules or interfaces. DNS and a route
// lookup are both necessary: looking up a hard-coded IP would be misleading.
func (s *Service) inspectControlRoute(ctx context.Context, destination string) OutboundRoute {
	v := OutboundRoute{Destination: destination, ObservedAt: time.Now().UTC(),
		Status: "UNKNOWN", Reason: "DNS or route unavailable"}
	if s.ip == "" || s.resolveIP == nil {
		return v
	}
	scoped, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	addrs, err := s.resolveIP(scoped, destination)
	if err != nil {
		v.Reason = "DNS resolution failed: " + compactReason(err.Error())
		return v
	}
	for _, addr := range addrs {
		ip := addr.IP.To4()
		if ip == nil {
			continue
		}
		v.ResolvedIP = ip.String()
		raw, truncated, err := s.run(scoped, s.ip, "-4", "route", "get", v.ResolvedIP)
		if err != nil {
			v.Reason = "route lookup failed: " + compactReason(err.Error())
			return v
		}
		if truncated {
			v.Reason = "route output truncated"
			return v
		}
		dev, source, via, err := parseRouteGet(string(raw))
		if err != nil {
			v.Reason = err.Error()
			return v
		}
		v.Interface, v.SourceIP, v.Gateway = dev, source, via
		v.Status, v.Reason = "OBSERVED", "route observed; failover independence requires a staged VPN-down E2E test"
		return v
	}
	v.Reason = "no IPv4 destination resolved"
	return v
}
