package controlplane

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// tailscale_ssh is an independent operator path: sshd reached over the
// Tailscale overlay. It is verified only from live kernel evidence: a listener
// that accepts overlay traffic, an established session from an online tailnet
// peer, and policy routing that sends overlay replies to Tailscale's table.

const (
	sshPort        = 22
	tailscaleTable = "52"
	// tailscaled marks only its own encapsulated WireGuard UDP with this bypass
	// mark; its stock rules 5210/5230/5250 precede rule 5270 by design.
	tailscaleBypassMark = "0x80000/0xff0000"
	tcpListen           = "0A"
	tcpEstablished      = "01"
	maxProcBytes        = 4 << 20
)

var (
	tailscaleV4 = mustCIDR("100.64.0.0/10")
	tailscaleV6 = mustCIDR("fd7a:115c:a1e0::/48")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func isTailscaleIP(ip net.IP) bool {
	return ip != nil && (tailscaleV4.Contains(ip) || tailscaleV6.Contains(ip))
}

type tcpSocket struct {
	Local, Remote         net.IP
	LocalPort, RemotePort int
	State                 string
}

// parseProcNetTCP parses /proc/net/tcp or /proc/net/tcp6. Malformed lines are skipped.
func parseProcNetTCP(raw []byte) []tcpSocket {
	var out []tcpSocket
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.HasSuffix(f[0], ":") {
			continue
		}
		local, lport, ok1 := decodeProcAddr(f[1])
		remote, rport, ok2 := decodeProcAddr(f[2])
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, tcpSocket{Local: local, Remote: remote, LocalPort: lport, RemotePort: rport, State: strings.ToUpper(f[3])})
	}
	return out
}

// decodeProcAddr decodes "HEXADDR:HEXPORT"; the address is a sequence of
// host-order (little-endian) 32-bit words, the port is big-endian hex.
func decodeProcAddr(s string) (net.IP, int, bool) {
	addr, port, ok := strings.Cut(s, ":")
	if !ok || (len(addr) != 8 && len(addr) != 32) {
		return nil, 0, false
	}
	b, err := hex.DecodeString(addr)
	if err != nil {
		return nil, 0, false
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return nil, 0, false
	}
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b), int(p), true
}

type ipRule struct {
	Pref   int
	Fields []string
}

func (r ipRule) value(key string) string {
	for i := 0; i+1 < len(r.Fields); i++ {
		if r.Fields[i] == key {
			return r.Fields[i+1]
		}
	}
	return ""
}

// parseIPRules parses `ip rule show` output ("5245:\tfrom all to 100.64.0.0/10 lookup 52").
func parseIPRules(raw []byte) []ipRule {
	var out []ipRule
	for _, line := range strings.Split(string(raw), "\n") {
		pref, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(pref)
		if err != nil {
			continue
		}
		out = append(out, ipRule{Pref: n, Fields: strings.Fields(rest)})
	}
	return out
}

// checkTailscaleRuleOrder requires the `to 100.64.0.0/10 lookup 52` rule to
// precede every fwmark rule that could steer overlay replies elsewhere.
// Tolerated before it: Tailscale's own bypass-mark rules, and fwmark rules
// whose table holds nothing but `local` routes (TPROXY delivery, never egress).
func checkTailscaleRuleOrder(rules []ipRule, localOnly func(table string) (bool, error)) (bool, string) {
	tsIndex := -1
	for i, r := range rules {
		if r.value("to") == tailscaleV4.String() && r.value("lookup") == tailscaleTable {
			tsIndex = i
			break
		}
	}
	if tsIndex < 0 {
		return false, "no 'to 100.64.0.0/10 lookup 52' policy rule"
	}
	ts := rules[tsIndex]
	for _, r := range rules[:tsIndex] {
		if mark := r.value("fwmark"); mark == "" || mark == tailscaleBypassMark {
			continue
		}
		if table := r.value("lookup"); table != "" && localOnly != nil {
			if ok, err := localOnly(table); err == nil && ok {
				continue
			}
		}
		return false, fmt.Sprintf("fwmark rule %d precedes tailscale rule %d", r.Pref, ts.Pref)
	}
	return true, ""
}

// localOnlyTable reports whether every route of a table is a `local` route.
func localOnlyTable(raw []byte) bool {
	seen := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "local ") {
			return false
		}
		seen = true
	}
	return seen
}

type sshEvidence struct {
	SelfIPs     []net.IP
	OnlinePeers map[string]bool // normalized IP strings of online tailnet peers
	Sockets     []tcpSocket
	RulesOK     bool
	RulesReason string
}

// evaluateTailscaleSSH applies conditions (a) listener, (b) established peer
// session, (c) policy-rule order. It returns verified and the first failure.
func evaluateTailscaleSSH(ev sshEvidence) (bool, string) {
	listening, session := false, false
	for _, s := range ev.Sockets {
		if s.LocalPort != sshPort {
			continue
		}
		switch s.State {
		case tcpListen:
			if s.Local.IsUnspecified() {
				listening = true
			}
			for _, ip := range ev.SelfIPs {
				if s.Local.Equal(ip) {
					listening = true
				}
			}
		case tcpEstablished:
			if isTailscaleIP(s.Remote) && ev.OnlinePeers[s.Remote.String()] {
				session = true
			}
		}
	}
	switch {
	case !listening:
		return false, "no sshd listener on 0.0.0.0, :: or the Tailscale IP port 22"
	case !session:
		return false, "no established SSH session from an online tailnet peer"
	case !ev.RulesOK:
		return false, ev.RulesReason
	}
	return true, ""
}

func (s *Service) tailscaleSSHPath(ctx context.Context, transportReady bool, selfIPs []net.IP, peers map[string]bool) Path {
	p := Path{ID: "tailscale_ssh", Kind: "overlay_ssh", External: true, State: "down",
		Reason: "tailscale transport not ready"}
	if len(selfIPs) > 0 {
		p.Address = net.JoinHostPort(selfIPs[0].String(), strconv.Itoa(sshPort))
	}
	if !transportReady {
		return p
	}
	p.TransportReady = true
	p.State = "unknown"
	if s.readFile == nil || s.ip == "" {
		p.Reason = "socket table or ip rule inspection unavailable"
		return p
	}
	ev := sshEvidence{SelfIPs: selfIPs, OnlinePeers: peers}
	raw, err := s.readFile("/proc/net/tcp")
	if err != nil {
		p.Reason = "read /proc/net/tcp: " + compactReason(err.Error())
		return p
	}
	ev.Sockets = parseProcNetTCP(raw)
	if raw6, err := s.readFile("/proc/net/tcp6"); err == nil {
		ev.Sockets = append(ev.Sockets, parseProcNetTCP(raw6)...)
	}
	rulesRaw, truncated, err := s.run(ctx, s.ip, "-4", "rule", "show")
	if err != nil || truncated {
		p.Reason = "ip rule inspection failed"
		return p
	}
	ev.RulesOK, ev.RulesReason = checkTailscaleRuleOrder(parseIPRules(rulesRaw), func(table string) (bool, error) {
		out, truncated, err := s.run(ctx, s.ip, "-4", "route", "show", "table", table)
		if err != nil || truncated {
			return false, fmt.Errorf("route table %s unavailable", table)
		}
		return localOnlyTable(out), nil
	})
	verified, reason := evaluateTailscaleSSH(ev)
	p.ManagementReady, p.Verified, p.Reason = verified, verified, reason
	p.State = stateWord(verified)
	return p
}

func readProcFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxProcBytes))
}
