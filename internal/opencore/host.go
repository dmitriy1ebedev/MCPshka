package opencore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// ExtraPath holds identifiers the host cannot show by itself (a TV's
// address, a MAC): [{"from": "...", "to": "..."}], service-owned, never in Git.
const ExtraPath = "/var/lib/mcpshka/opencore/identifiers.json"

// HostRules derives the owner identifiers from the live host (state comes
// from the host, not from a hand list): its LAN and tailnet addresses, its
// MagicDNS suffix and the tailnet peers' addresses, plus ExtraPath. Each
// gets a neutral placeholder of the same class, numbered in sorted order.
func HostRules(ctx context.Context) []Rule {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var own, peers []string
	suffix := ""
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifs {
			if n, ok := a.(*net.IPNet); ok {
				own = append(own, n.IP.String())
			}
		}
	}
	if raw, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output(); err == nil {
		var st struct {
			MagicDNSSuffix string
			Self           struct{ TailscaleIPs []string }
			Peer           map[string]struct{ TailscaleIPs []string }
		}
		if json.Unmarshal(raw, &st) == nil {
			suffix = strings.TrimSuffix(st.MagicDNSSuffix, ".")
			own = append(own, st.Self.TailscaleIPs...)
			for _, p := range st.Peer {
				peers = append(peers, p.TailscaleIPs...)
			}
		}
	}
	rules := placeholders(own, peers)
	if strings.HasSuffix(suffix, ".ts.net") {
		rules = append(rules, Rule{From: suffix, To: "example.ts.net"})
	}
	if raw, err := os.ReadFile(ExtraPath); err == nil {
		var extra []Rule
		if json.Unmarshal(raw, &extra) == nil {
			rules = append(rules, extra...)
		}
	}
	return normalize(rules)
}

// placeholders maps addresses to the same class: LAN 192.168/16 →
// 192.168.77.x, 10/8 → 10.77.0.x, tailnet 100.64/10 → 100.77.0.x (own) and
// 100.77.1.x (peers), IPv6 ULA/tailnet → fd7a:115c:a1e0::77:x. Loopback,
// link-local and Docker's bridge (generic, not the owner's) are kept.
func placeholders(own, peers []string) []Rule {
	var out []Rule
	n := map[string]int{}
	add := func(ips []string, group string) {
		sort.Strings(ips)
		for _, s := range ips {
			ip := net.ParseIP(s)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			class := ""
			switch v4 := ip.To4(); {
			case v4 != nil && v4[0] == 192 && v4[1] == 168:
				class = "192.168.77.%d"
			case v4 != nil && v4[0] == 10:
				class = "10.77.0.%d"
			case v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64:
				class = "100.77." + map[string]string{"own": "0", "peer": "1"}[group] + ".%d"
			case v4 == nil && (ip[0]&0xfe == 0xfc):
				class = "fd7a:115c:a1e0::77:%d"
			default:
				continue // public or Docker bridge: not an owner identifier here
			}
			n[class]++
			out = append(out, Rule{From: ip.String(), To: fmt.Sprintf(class, 9+n[class])})
		}
	}
	add(own, "own")
	add(peers, "peer")
	return out
}
