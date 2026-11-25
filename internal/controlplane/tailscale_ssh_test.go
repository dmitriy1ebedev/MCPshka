package controlplane

import (
	"context"
	"errors"
	"net"
	"testing"
)

// Real kernel encoding: 0.0.0.0:22 LISTEN, 100.77.0.10:22 <- 100.77.1.11:57234 ESTABLISHED.
const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0
   1: 16995964:0016 72394864:DF92 01 00000000:00000000 02:000A7B2E 00000000     0        0 2 2 0 20 4 30 10 -1
`

const procTCP6 = `  sl  local_address                         remote_address                        st
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3
`

const liveRules = "0:\tfrom all lookup local\n" +
	"5095:\tfrom all fwmark 0x7a00/0xff00 lookup 177\n" +
	"5210:\tfrom all fwmark 0x80000/0xff0000 lookup main\n" +
	"5245:\tfrom all to 100.64.0.0/10 lookup 52\n" +
	"5250:\tfrom all fwmark 0x80000/0xff0000 unreachable\n" +
	"32766:\tfrom all lookup main\n"

func TestParseProcNetTCP(t *testing.T) {
	socks := parseProcNetTCP([]byte(procTCP))
	if len(socks) != 2 {
		t.Fatalf("sockets: %+v", socks)
	}
	if !socks[0].Local.IsUnspecified() || socks[0].LocalPort != 22 || socks[0].State != tcpListen {
		t.Fatalf("listener: %+v", socks[0])
	}
	est := socks[1]
	if est.Local.String() != "100.77.0.10" || est.Remote.String() != "100.77.1.11" ||
		est.RemotePort != 0xDF92 || est.State != tcpEstablished {
		t.Fatalf("session: %+v", est)
	}
	v6 := parseProcNetTCP([]byte(procTCP6))
	if len(v6) != 1 || !v6[0].Local.IsUnspecified() || v6[0].LocalPort != 22 {
		t.Fatalf("tcp6: %+v", v6)
	}
	ip, _, ok := decodeProcAddr("0000000000000000FFFF000072394864:0016")
	if !ok || ip.String() != "100.77.1.11" {
		t.Fatalf("v4-mapped: %v %v", ip, ok)
	}
	if got := parseProcNetTCP([]byte("   0: zz:0016 00000000:0000 0A\n")); len(got) != 0 {
		t.Fatalf("malformed accepted: %+v", got)
	}
}

func TestTailscaleRuleOrder(t *testing.T) {
	localOnly := func(table string) (bool, error) { return table == "177", nil }
	for _, tc := range []struct {
		name, rules string
		want        bool
	}{
		{"tailscale before divergent fwmark", "5245:\tfrom all to 100.64.0.0/10 lookup 52\n5250:\tfrom all fwmark 0x1 lookup 110\n", true},
		{"preceding tproxy-local fwmark tolerated", "5095:\tfrom all fwmark 0x7a00/0xff00 lookup 177\n5245:\tfrom all to 100.64.0.0/10 lookup 52\n", true},
		{"live host order with tailscale bypass rules", liveRules, true},
		{"preceding foreign fwmark lookup main", "5200:\tfrom all fwmark 0x1/0xff lookup main\n5245:\tfrom all to 100.64.0.0/10 lookup 52\n", false},
		{"preceding fwmark without lookup", "5000:\tfrom all fwmark 0x1 prohibit\n5245:\tfrom all to 100.64.0.0/10 lookup 52\n", false},
		{"missing tailscale rule", "5270:\tfrom all lookup 52\n", false},
		{"wrong table", "5245:\tfrom all to 100.64.0.0/10 lookup main\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := checkTailscaleRuleOrder(parseIPRules([]byte(tc.rules)), localOnly)
			if ok != tc.want || (!ok && reason == "") {
				t.Fatalf("ok=%v reason=%q", ok, reason)
			}
		})
	}
	if ok, _ := checkTailscaleRuleOrder(parseIPRules([]byte("5095:\tfrom all fwmark 0x7a00 lookup 177\n5245:\tfrom all to 100.64.0.0/10 lookup 52\n")),
		func(string) (bool, error) { return false, errors.New("unavailable") }); ok {
		t.Fatal("unknown table content tolerated")
	}
	if !localOnlyTable([]byte("local default dev lo scope host\n")) || localOnlyTable([]byte("default via 10.0.0.1 dev eth0\n")) || localOnlyTable(nil) {
		t.Fatal("localOnlyTable")
	}
}

func TestEvaluateTailscaleSSH(t *testing.T) {
	self := []net.IP{net.ParseIP("100.77.0.10")}
	peers := map[string]bool{"100.77.1.11": true}
	socks := parseProcNetTCP([]byte(procTCP))
	if ok, reason := evaluateTailscaleSSH(sshEvidence{SelfIPs: self, OnlinePeers: peers, Sockets: socks, RulesOK: true}); !ok {
		t.Fatalf("expected verified: %s", reason)
	}
	cases := map[string]sshEvidence{
		"no listener":       {SelfIPs: self, OnlinePeers: peers, Sockets: socks[1:], RulesOK: true},
		"lan-only listener": {SelfIPs: self, OnlinePeers: peers, Sockets: append([]tcpSocket{{Local: net.ParseIP("192.168.77.10"), LocalPort: 22, State: tcpListen}}, socks[1:]...), RulesOK: true},
		"peer offline":      {SelfIPs: self, OnlinePeers: map[string]bool{}, Sockets: socks, RulesOK: true},
		"non-tailnet peer":  {SelfIPs: self, OnlinePeers: map[string]bool{"192.168.0.5": true}, Sockets: []tcpSocket{socks[0], {Local: self[0], Remote: net.ParseIP("192.168.0.5"), LocalPort: 22, State: tcpEstablished}}, RulesOK: true},
		"rules wrong":       {SelfIPs: self, OnlinePeers: peers, Sockets: socks, RulesReason: "fwmark rule 5210 precedes tailscale rule 5245"},
	}
	for name, ev := range cases {
		if ok, reason := evaluateTailscaleSSH(ev); ok || reason == "" {
			t.Fatalf("%s: ok=%v reason=%q", name, ok, reason)
		}
	}
	tsListen := []tcpSocket{{Local: self[0], LocalPort: 22, State: tcpListen}, socks[1]}
	if ok, _ := evaluateTailscaleSSH(sshEvidence{SelfIPs: self, OnlinePeers: peers, Sockets: tsListen, RulesOK: true}); !ok {
		t.Fatal("listener bound to the Tailscale IP rejected")
	}
}

// sshControlService extends the base fake with live evidence for tailscale_ssh.
// The iPhone peer has DNSName so it counts as a real tailnet peer.
func sshControlService(t *testing.T, rules string) *Service {
	s := testControlService(t)
	base := s.run
	s.ip = "/fake/ip"
	s.run = func(ctx context.Context, path string, args ...string) ([]byte, bool, error) {
		switch path {
		case "/fake/tailscale":
			return []byte(`{"BackendState":"Running","TailscaleIPs":["100.77.0.10"],"Self":{"Online":true},"Peer":{"nodekey:phone":{"HostName":"localhost","DNSName":"phone.example.ts.net.","OS":"iOS","TailscaleIPs":["100.77.1.11"],"Online":true}}}`), false, nil
		case "/fake/ip":
			if len(args) > 1 && args[1] == "rule" {
				return []byte(rules), false, nil
			}
			if args[len(args)-1] == "177" {
				return []byte("local default dev lo scope host\n"), false, nil
			}
			return []byte("default via 192.168.0.1 dev eth0\n"), false, nil
		}
		return base(ctx, path, args...)
	}
	s.readFile = func(path string) ([]byte, error) {
		switch path {
		case "/proc/net/tcp":
			return []byte(procTCP), nil
		case "/proc/net/tcp6":
			return []byte(procTCP6), nil
		}
		return nil, errors.New("unexpected file")
	}
	return s
}

const badRules = "5100:\tfrom all fwmark 0x1/0xff lookup 110\n5245:\tfrom all to 100.64.0.0/10 lookup 52\n"

const goodRules = "5095:\tfrom all fwmark 0x7a00/0xff00 lookup 177\n5245:\tfrom all to 100.64.0.0/10 lookup 52\n5250:\tfrom all fwmark 0x80000/0xff0000 unreachable\n"

func TestSnapshotVerifiesTailscaleSSH(t *testing.T) {
	snap, err := sshControlService(t, goodRules).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := findPath(snap, "tailscale_ssh")
	if !ok || p.Kind != "overlay_ssh" || !p.External || !p.TransportReady || !p.ManagementReady || !p.Verified || p.Reason != "" {
		t.Fatalf("tailscale_ssh: %+v", p)
	}
	if len(snap.VerifiedBackups) != 1 || snap.VerifiedBackups[0] != "tailscale_ssh" {
		t.Fatalf("backups: %+v", snap.VerifiedBackups)
	}
	// Default fake lacks socket/rule evidence: never verified, reason given.
	base, _ := testControlService(t).Snapshot(context.Background())
	if p, _ := findPath(base, "tailscale_ssh"); p.Verified || p.ManagementReady || p.Reason == "" {
		t.Fatalf("unverified evidence promoted: %+v", p)
	}
}

func TestPreflightFirewallWithTailscaleSSHBackup(t *testing.T) {
	req := MutationRequest{Kind: "firewall", ViaProvider: "lane", AffectedPaths: []string{"openai_tunnel"}, Staged: true, RollbackSeconds: 600}
	for _, tc := range []struct {
		name    string
		rules   string
		mutate  func(*MutationRequest)
		allowed bool
	}{
		{name: "backup present", rules: goodRules, allowed: true},
		{name: "live rule order", rules: liveRules, allowed: true},
		{name: "backup absent", rules: badRules},
		{name: "backup affected", rules: goodRules, mutate: func(r *MutationRequest) { r.AffectedPaths = []string{"openai_tunnel", "tailscale_ssh"} }},
		{name: "overlay affected", rules: goodRules, mutate: func(r *MutationRequest) { r.AffectedPaths = []string{"openai_tunnel", "tailscale"} }},
		{name: "rollback too long", rules: goodRules, mutate: func(r *MutationRequest) { r.RollbackSeconds = 901 }},
		{name: "not staged", rules: goodRules, mutate: func(r *MutationRequest) { r.Staged = false }},
		{name: "direct provider", rules: goodRules, mutate: func(r *MutationRequest) { r.ViaProvider = "direct" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			got, err := sshControlService(t, tc.rules).Preflight(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if got.Allowed != tc.allowed || got.BlastRadius.VerifiedBackupExists != (tc.allowed || tc.name == "rollback too long" || tc.name == "not staged" || tc.name == "direct provider") {
				t.Fatalf("decision=%s allowed=%v blast=%+v reason=%s", got.Decision, got.Allowed, got.BlastRadius, got.Reason)
			}
			if tc.allowed && got.Decision != DecisionGuarded {
				t.Fatalf("decision=%s", got.Decision)
			}
			if !tc.allowed && got.Decision == DecisionGuarded {
				t.Fatalf("unsafe decision=%s reason=%s", got.Decision, got.Reason)
			}
		})
	}
}
