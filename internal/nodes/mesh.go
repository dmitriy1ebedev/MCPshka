package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// MeshObservation separates a verified Tailscale peer identity and a live
// reachability probe from enrollment, SSH authorization and worker readiness.
// No routing, ACL, exit-node or VPN configuration is ever changed here.
type MeshObservation struct {
	Node            string    `json:"node"`
	PeerIP          string    `json:"peer_ip"`
	PeerKey         string    `json:"peer_key"`
	ObservedAt      time.Time `json:"observed_at"`
	IdentityMatched bool      `json:"identity_matched"`
	TransportOnline bool      `json:"transport_online"`
	PeerReachable   bool      `json:"peer_reachable"`
	SSHAuthorized   bool      `json:"ssh_authorized"`
	WorkerReady     bool      `json:"worker_ready"`
	Detail          string    `json:"detail"`
}

var tailscaleBinary = "/usr/bin/tailscale"

func validOverlayIP(s string) bool {
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	v4 := netip.MustParsePrefix("100.64.0.0/10")
	v6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	return (ip.Is4() && v4.Contains(ip)) || (ip.Is6() && v6.Contains(ip))
}

// ObserveMesh uses an owner-pinned Tailscale node key rather than an IP alone.
// A peer that merely appears online is never promoted to an enrolled worker.
func ObserveMesh(ctx context.Context, node, ip, expectedKey string) (MeshObservation, error) {
	o := MeshObservation{Node: node, PeerIP: ip, PeerKey: expectedKey, ObservedAt: time.Now().UTC(),
		Detail: "not verified"}
	if err := check(node); err != nil {
		return o, err
	}
	if _, err := address(node); err != nil {
		return o, err
	}
	if !validOverlayIP(ip) {
		return o, errors.New("peer must have a Tailscale overlay IP")
	}
	if !strings.HasPrefix(expectedKey, "nodekey:") || len(expectedKey) < 16 ||
		len(expectedKey) > 128 || strings.ContainsAny(expectedKey, " \t\r\n") {
		return o, errors.New("expected Tailscale nodekey is required and must be pinned out of band")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	raw, err := run(ctx, nil, tailscaleBinary, "status", "--json")
	if err != nil {
		return o, fmt.Errorf("tailscale status: %w", err)
	}
	var status struct {
		BackendState string
		Self         struct{ Online bool }
		Peer         map[string]struct {
			TailscaleIPs   []string
			Online         bool
			ExitNode       bool
			ExitNodeOption bool
		}
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return o, fmt.Errorf("tailscale JSON: %w", err)
	}
	if status.BackendState != "Running" || !status.Self.Online {
		o.Detail = "local Tailscale transport is not running"
		return o, nil
	}
	peer, ok := status.Peer[expectedKey]
	if !ok {
		o.Detail = "pinned Tailscale peer key absent (refusing another identity)"
		return o, nil
	}
	for _, candidate := range peer.TailscaleIPs {
		if candidate == ip {
			o.IdentityMatched = true
			break
		}
	}
	if !o.IdentityMatched {
		o.Detail = "pinned Tailscale identity does not own the requested overlay IP"
		return o, nil
	}
	if peer.ExitNode || peer.ExitNodeOption {
		o.Detail = "peer advertises an exit-node role; mesh onboarding is on hold"
		return o, nil
	}
	o.TransportOnline = peer.Online
	if !peer.Online {
		o.Detail = "peer identity matched but peer is offline"
		return o, nil
	}
	pong, err := run(ctx, nil, tailscaleBinary, "ping", "-c", "1", "--timeout", "4s", ip)
	if err != nil {
		o.Detail = "peer online in coordination but active probe failed"
		return o, nil
	}
	if !strings.Contains(string(pong), "pong from") {
		o.Detail = "unexpected Tailscale ping response"
		return o, nil
	}
	o.PeerReachable = true
	o.Detail = "pinned peer reached; ACL/SSH authorization and worker capabilities still unverified"
	return o, nil
}
