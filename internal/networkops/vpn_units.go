package networkops

import "strings"

// parseVPNUnits interprets bounded, untrusted systemctl --no-legend output.
// It never executes commands, reads credentials or changes network state.
func parseVPNUnits(raw []byte) []VPN {
	if len(raw) > 64*1024 {
		raw = raw[:64*1024]
	}
	out := make([]VPN, 0, 8)
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		unit := fields[0]
		if len(unit) > 128 || !strings.HasSuffix(unit, ".service") || seen[unit] {
			continue
		}
		lower := strings.ToLower(unit)
		kind := ""
		switch {
		case strings.HasPrefix(lower, "openvpn"), strings.HasPrefix(lower, "ovpn-"):
			kind = "openvpn"
		case strings.HasPrefix(lower, "reverse-ssh"), strings.HasPrefix(lower, "reverse_ssh"):
			kind = "reverse_ssh"
		case strings.HasPrefix(lower, "autossh"):
			kind = "autossh"
		case strings.HasPrefix(lower, "amneziawg"):
			kind = "amneziawg"
		case strings.HasPrefix(lower, "wg-quick"):
			kind = "wireguard"
		}
		if kind == "" {
			continue
		}
		state := strings.ToLower(fields[2])
		if state != "active" && state != "inactive" && state != "failed" && state != "activating" && state != "deactivating" {
			continue
		}
		out = append(out, VPN{Interface: unit, Kind: kind, State: state})
		seen[unit] = true
		if len(out) >= 32 {
			break
		}
	}
	return out
}
