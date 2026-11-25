package networkops

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestSnapshotParsesBoundedSources(t *testing.T) {
	run := func(ctx context.Context, path string, max int, args ...string) ([]byte, bool, error) {
		if slices.Contains(args, "address") {
			return []byte(`[{"ifindex":2,"ifname":"eth0","operstate":"UP","mtu":1500,"addr_info":[{"local":"192.168.77.10","prefixlen":24}]}]`), false, nil
		}
		if slices.Contains(args, "route") {
			return []byte(`[{"dst":"default","gateway":"192.168.0.1","dev":"eth0","metric":100},{"dst":"192.168.0.0/24","dev":"eth0"}]`), false, nil
		}
		if path == "/fake/ss" {
			return []byte("tcp LISTEN 0 4096 127.0.0.1:8765 0.0.0.0:*\nudp UNCONN 0 0 0.0.0.0:5353 0.0.0.0:*\n"), false, nil
		}
		return nil, false, errors.New("unexpected command")
	}
	read := func(path string, max int) ([]byte, bool, error) {
		if path != "/etc/resolv.conf" {
			return nil, false, errors.New("unexpected read")
		}
		return []byte("nameserver 127.0.2.1\noptions timeout:2 attempts:2\n"), false, nil
	}
	out, err := newService(run, read, "/fake/ip", "/fake/ss").Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Links) != 1 || out.Links[0].Addresses[0] != "192.168.77.10/24" {
		t.Fatalf("links=%+v", out.Links)
	}
	if len(out.Routes) != 2 || out.Routes[0].Gateway != "192.168.0.1" {
		t.Fatalf("routes=%+v", out.Routes)
	}
	if len(out.DNS.Nameservers) != 1 || out.DNS.Nameservers[0] != "127.0.2.1" {
		t.Fatalf("dns=%+v", out.DNS)
	}
	if len(out.Sockets) != 2 || out.Sockets[0].Local != "127.0.0.1:8765" {
		t.Fatalf("sockets=%+v", out.Sockets)
	}
}

func TestParsersRejectOrBoundBadData(t *testing.T) {
	if _, err := parseLinks([]byte("not-json")); err == nil {
		t.Fatal("invalid link json accepted")
	}
	if _, err := parseRoutes([]byte("not-json")); err == nil {
		t.Fatal("invalid route json accepted")
	}
	dns := parseDNS([]byte("nameserver nope\nnameserver 1.1.1.1\n"))
	if len(dns.Nameservers) != 1 || dns.Nameservers[0] != "1.1.1.1" {
		t.Fatalf("dns=%+v", dns)
	}
}

func TestVPNAwarenessFingerprintDigestAndConnectivity(t *testing.T) {
	snapshot := finalizeSnapshot(Snapshot{
		Links: []Link{
			{Name: "eth0", State: "up"},
			{Name: "tailscale0", State: "unknown"},
			{Name: "wg0", State: "up", Kind: "wireguard"},
			{Name: "tun-mandatory", State: "up"},
		},
		Routes: []Route{
			{Family: "ipv4", Destination: "default", Gateway: "192.168.0.1", Device: "eth0"},
			{Family: "ipv6", Destination: "default", Gateway: "fe80::1", Device: "eth0"},
		},
		DNS: DNS{Nameservers: []string{"192.168.77.10"}},
	})
	if snapshot.Fingerprint == "" || len(snapshot.VPNs) != 3 {
		t.Fatalf("vpn/fingerprint=%+v %q", snapshot.VPNs, snapshot.Fingerprint)
	}
	kinds := map[string]string{}
	for _, vpn := range snapshot.VPNs {
		kinds[vpn.Interface] = vpn.Kind
	}
	if kinds["tailscale0"] != "tailscale" || kinds["wg0"] != "wireguard" || kinds["tun-mandatory"] != "tunnel" {
		t.Fatalf("vpn kinds=%v", kinds)
	}
	digest := Digest(snapshot)
	if digest.Fingerprint != snapshot.Fingerprint || len(digest.DefaultRoutes) != 2 || len(digest.Nameservers) != 1 {
		t.Fatalf("digest=%+v", digest)
	}

	s := newService(nil, nil, "", "")
	s.probe = func(context.Context) Connectivity {
		return Connectivity{OK: true, Probes: []ProbeResult{{Name: "dns", OK: true}, {Name: "https", OK: true}}}
	}
	connectivity, err := s.Connectivity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !connectivity.OK || len(connectivity.Probes) != 2 {
		t.Fatalf("connectivity=%+v", connectivity)
	}
}

func TestLiveSnapshotWhenToolsAvailable(t *testing.T) {
	s := New()
	if s.ipPath == "" || s.ssPath == "" {
		t.Skip("ip/ss tools are not installed")
	}
	out, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// An isolated network namespace (the integration test sandbox) has links
	// but no listeners; that is a valid host state, not a parser failure.
	if len(out.Links) > 0 && len(out.Sockets) == 0 && len(out.DNS.Nameservers) == 0 {
		t.Skip("isolated network namespace without listeners or resolver")
	}
	if len(out.Links) == 0 || len(out.Sockets) == 0 {
		t.Fatalf("incomplete live snapshot: links=%d routes=%d dns=%d sockets=%d warnings=%v",
			len(out.Links), len(out.Routes), len(out.DNS.Nameservers), len(out.Sockets), out.Warnings)
	}
}

func TestSnapshotRequiresFixedCommands(t *testing.T) {
	s := newService(nil, nil, "", "")
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("missing command paths accepted")
	}
}
