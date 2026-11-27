package networkops

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointProbeRefusesForeignHosts(t *testing.T) {
	for _, u := range []string{"https://example.com", "https://8.8.8.8", "ftp://h.ts.net", "https://u:p@h.ts.net"} {
		if _, err := ProbeEndpoint(context.Background(), u, ""); err == nil {
			t.Fatalf("%s must be refused", u)
		}
	}
	if _, err := ProbeEndpoint(context.Background(), "https://h.ts.net", "vpn"); err == nil {
		t.Fatal("unknown via must be refused")
	}
}

func TestEndpointProbeReportsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	out, err := ProbeEndpoint(context.Background(), srv.URL, "")
	if err != nil || !out.OK || out.StatusCode != 204 || out.Via != "lan" || !strings.HasPrefix(out.Dialed, "127.0.0.1:") {
		t.Fatalf("%+v %v", out, err)
	}
	if out.TotalMs < 0 || out.DNSMillis > out.TotalMs {
		t.Fatalf("timings: %+v", out)
	}
	srv.Close()
	out, _ = ProbeEndpoint(context.Background(), srv.URL, "lan")
	if out.OK || out.Stage != "connect" {
		t.Fatalf("closed port must fail at connect: %+v", out)
	}
}

func TestMCP246ControlPlaneFixedTargetsAndDNSPin(t *testing.T) {
	for _, tc := range []struct{ target, host, path string }{
		{"tailscale_control", "controlplane.tailscale.com", "/key?v=148"},
		{"tailscale_login", "login.tailscale.com", "/"},
	} {
		h, p, err := controlHTTPSHost(tc.target)
		if err != nil || h != tc.host || p != tc.path {
			t.Fatalf("target %q: %q %q %v", tc.target, h, p, err)
		}
	}
	for _, forbidden := range []string{"https://example.com", "controlplane.tailscale.com", "tailscale_status", "login.tailscale.com", ""} {
		if _, _, err := controlHTTPSHost(forbidden); err == nil {
			t.Fatalf("arbitrary HTTPS target accepted: %q", forbidden)
		}
	}
	trusted := []net.IP{net.ParseIP("192.200.0.110"), net.ParseIP("192.200.0.115")}
	if got, err := selectControlDNSIP(trusted, "192.200.0.115"); err != nil || got.String() != "192.200.0.115" {
		t.Fatalf("forced live A record refused: %v %v", got, err)
	}
	if got, err := selectControlDNSIP(trusted, ""); err != nil || got.String() != "192.200.0.110" {
		t.Fatalf("default A record refused: %v %v", got, err)
	}
	for _, forced := range []string{"127.0.0.1", "192.168.0.1", "192.200.0.111", "192.200.0.110:443", "::1", "192.200.0.115%eth0", "8.8.8.8"} {
		if _, err := selectControlDNSIP(trusted, forced); err == nil {
			t.Fatalf("foreign, malformed or private dial_ip accepted: %q", forced)
		}
	}
	for _, bad := range [][]net.IP{{}, {net.ParseIP("127.0.0.1")}, {net.ParseIP("192.168.0.1")}, {net.ParseIP("100.64.0.1")}} {
		if _, err := selectControlDNSIP(bad, ""); err == nil {
			t.Fatalf("private or missing DNS answer accepted: %v", bad)
		}
	}
}

func TestMCP246ControlProbeFailsClosedBeforeAnyDial(t *testing.T) {
	old := lookupControlHTTPSIP
	defer func() { lookupControlHTTPSIP = old }()
	calls := 0
	lookupControlHTTPSIP = func(_ context.Context, network, host string) ([]net.IP, error) {
		calls++
		if network != "ip4" || host != "controlplane.tailscale.com" {
			t.Fatalf("unexpected DNS request: %s %s", network, host)
		}
		return []net.IP{net.ParseIP("192.200.0.115")}, nil
	}
	if _, err := ProbeControlHTTPS(context.Background(), "https://other.com", ""); err == nil || calls != 0 {
		t.Fatal("unapproved target performed DNS/network access")
	}
	result, err := ProbeControlHTTPS(context.Background(), "tailscale_control", "8.8.8.8")
	if err != nil || calls != 1 || result.OK || result.Stage != "dns" || result.Source != "H96" || result.Dialed != "" {
		t.Fatalf("foreign forced IP reached network: %+v %v calls=%d", result, err, calls)
	}
}

func TestTailnetLookupUsesTailscaledStatus(t *testing.T) {
	old := tailscaleStatus
	defer func() { tailscaleStatus = old }()
	tailscaleStatus = func(context.Context) ([]byte, error) {
		return []byte(`{"Self":{"DNSName":"box.tail1.ts.net.","TailscaleIPs":["fd7a::1","100.64.0.5"]},"Peer":{"k":{"DNSName":"phone.tail1.ts.net.","TailscaleIPs":["100.64.0.9"]}}}`), nil
	}
	for host, want := range map[string]string{"box.tail1.ts.net": "100.64.0.5", "phone.tail1.ts.net": "100.64.0.9"} {
		if got, err := tailnetLookup(context.Background(), host); err != nil || got[0] != want {
			t.Fatalf("%s: %v %v", host, got, err)
		}
	}
	if _, err := tailnetLookup(context.Background(), "other.ts.net"); err == nil {
		t.Fatal("unknown name must not resolve")
	}
}
