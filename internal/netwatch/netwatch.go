// Package netwatch classifies a control-path outage from independent probes
// (MCP-096) so recovery acts on the failed layer: restarting the tunnel
// client is pointless when the WAN is down. Switching needs a debounced,
// confirmed classification and stays inside a restart budget; only actions
// of the owner-approved plan are ever proposed.
package netwatch

import "time"

type Probe struct {
	At            time.Time `json:"at"`
	WANDirect     bool      `json:"wan_direct"`     // TCP 443 to a public anchor via eth0
	DNS           bool      `json:"dns"`            // resolver answers for the control host
	VPN           bool      `json:"vpn"`            // commercial VPN interface passes traffic
	TunnelControl bool      `json:"tunnel_control"` // TCP 443 to the MCP tunnel endpoint
	TunnelClient  bool      `json:"tunnel_client"`  // tunnel-client healthz/readyz
	Tailscale     bool      `json:"tailscale"`      // tailscale peer ping to the emergency overlay
}

const (
	OK           = "OK"
	WANDown      = "WAN_DOWN"
	DNSDown      = "DNS"
	VPNOnly      = "VPN_ONLY"
	TunnelPath   = "TUNNEL_PATH"   // WAN fine, tunnel endpoint unreachable (route/policy)
	TunnelClient = "TUNNEL_CLIENT" // path fine, client process unhealthy
	OverlayOnly  = "TAILSCALE"
)

// Classify names the lowest failed layer; upper layers are not blamed for it.
func Classify(p Probe) string {
	switch {
	case !p.WANDirect:
		return WANDown
	case !p.DNS:
		return DNSDown
	case !p.TunnelControl:
		return TunnelPath
	case !p.TunnelClient:
		return TunnelClient
	case !p.VPN:
		return VPNOnly
	case !p.Tailscale:
		return OverlayOnly
	}
	return OK
}

// Plan is the owner-approved recovery per class; anything else only alerts.
var Plan = map[string]string{
	TunnelClient: "restart tunnel-client.service",
	TunnelPath:   "network_stage_apply control-plane exclusion route (auto-rollback)",
	OverlayOnly:  "restart tailscaled.service",
}

type Decision struct {
	Class      string    `json:"class"`
	Confirmed  bool      `json:"confirmed"`
	Action     string    `json:"action,omitempty"`
	Alert      string    `json:"alert,omitempty"`
	LastGood   time.Time `json:"last_good"`
	Fresh      bool      `json:"fresh"`
	BudgetLeft int       `json:"restart_budget_left"`
}

type Watch struct {
	Debounce int           // consecutive equal probes before acting
	Budget   int           // actions per Window
	Window   time.Duration // restart budget window
	history  []string
	actions  []time.Time
	lastGood time.Time
}

// Observe feeds one probe. Probes older than 5 minutes are stale: no action.
func (w *Watch) Observe(p Probe, now time.Time) Decision {
	class := Classify(p)
	if class == OK {
		w.lastGood = p.At
	}
	w.history = append(w.history, class)
	if len(w.history) > w.Debounce {
		w.history = w.history[len(w.history)-w.Debounce:]
	}
	kept := w.actions[:0]
	for _, a := range w.actions {
		if now.Sub(a) < w.Window {
			kept = append(kept, a)
		}
	}
	w.actions = kept
	d := Decision{Class: class, LastGood: w.lastGood, Fresh: now.Sub(p.At) <= 5*time.Minute, BudgetLeft: w.Budget - len(w.actions)}
	d.Confirmed = len(w.history) == w.Debounce
	for _, h := range w.history {
		if h != class {
			d.Confirmed = false
		}
	}
	if class == OK || !d.Confirmed || !d.Fresh {
		return d
	}
	action, approved := Plan[class]
	switch {
	case !approved:
		d.Alert = class + ": no approved automatic action (physical link/ISP/VPN provider); alert the owner, keep the kill switch"
	case d.BudgetLeft <= 0:
		d.Alert = class + ": restart budget exhausted; alert the owner instead of looping"
	default:
		d.Action = action
		w.actions = append(w.actions, now)
		d.BudgetLeft--
	}
	return d
}
