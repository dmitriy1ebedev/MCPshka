package systemdsvc

import "strings"

// NetworkStatusUnit admits status (never logs or actions) of the fixed
// VPN/LANE unit families: diagnosing OpenVPN, WireGuard, Xray or LANE was
// POLICY_DENIED and pushed owners to Termius (r20 MCP-097). Journals stay
// configured-only because they may carry peer addresses; Lane units are
// the exception (laneUnit).
func NetworkStatusUnit(name string) bool {
	if !validServiceName(name) {
		return false
	}
	switch name {
	case "openvpn.service", "tailscaled.service", "laned.service", "xray.service":
		return true
	}
	for _, prefix := range []string{"openvpn@", "openvpn-client@", "openvpn-server@", "wg-quick@", "lane-", "xray"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix)+len(".service") {
			return true
		}
	}
	return false
}

// OwnProjectJournal: units of the owner's projects (Lane, DIAG) by name prefix.
// Transport engines stay closed: their journals can carry client ids and keys.
func OwnProjectJournal(unit string) bool {
	for _, s := range []string{"xray", "sing-box", "singbox", "awg", "wireguard", "openvpn", "strongswan", "ikev2", "tun2"} {
		if strings.Contains(unit, s) {
			return false
		}
	}
	for _, p := range []string{"lane-", "laned", "diag"} {
		if strings.HasPrefix(unit, p) {
			return true
		}
	}
	return false
}

// laneUnit names a unit of the owner's Lane project.
func laneUnit(name string) bool {
	return validServiceName(name) && (name == "laned.service" || strings.HasPrefix(name, "lane-") && len(name) > len("lane-.service"))
}
