package networkops

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// EndpointProbe answers "does my own public/tailnet address answer?" from the
// host itself (MCP-116). The local dnsmasq hands the LAN IP back for MagicDNS
// names, so a plain curl to https://<host>.ts.net:8447 from H96 tested the LAN
// path, not the tailnet. The probe resolves through the network named in via.
// Targets are limited to the owner's own endpoints: *.ts.net names and
// private/CGNAT/loopback literals, never arbitrary internet hosts.
type EndpointProbe struct {
	URL        string    `json:"url"`
	Via        string    `json:"via"`
	Resolver   string    `json:"resolver"`
	Addresses  []string  `json:"addresses,omitempty"`
	Dialed     string    `json:"dialed,omitempty"`
	DNSMillis  int64     `json:"dns_ms"`
	TotalMs    int64     `json:"total_ms"`
	TLSVersion string    `json:"tls_version,omitempty"`
	TLSValid   bool      `json:"tls_valid"`
	CertExpiry time.Time `json:"cert_expires_at,omitzero"`
	StatusCode int       `json:"status_code,omitempty"`
	OK         bool      `json:"ok"`
	Stage      string    `json:"failed_stage,omitempty"`
	Error      string    `json:"error,omitempty"`
}

const endpointTimeout = 10 * time.Second

// ControlHTTPSProbe is an explicit H96-side observation, not evidence about
// an iPhone on LTE or about the security of a VPN failover path (MCP-246).
// An IP override is admitted only when it belongs to this host's current DNS
// answer; arbitrary-IP network scanning is outside this read-only contract.
type ControlHTTPSProbe struct {
	Target      string   `json:"target"`
	Source      string   `json:"source"`
	Host        string   `json:"host"`
	DNSIPs      []string `json:"dns_ips,omitempty"`
	Dialed      string   `json:"dialed,omitempty"`
	DNSMillis   int64    `json:"dns_ms"`
	TotalMillis int64    `json:"total_ms"`
	TLSValid    bool     `json:"tls_valid"`
	TLSVersion  string   `json:"tls_version,omitempty"`
	StatusCode  int      `json:"status_code,omitempty"`
	OK          bool     `json:"ok"`
	Stage       string   `json:"failed_stage,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func controlHTTPSHost(target string) (host, path string, err error) {
	switch target {
	case "tailscale_control":
		return "controlplane.tailscale.com", "/key?v=148", nil
	case "tailscale_login":
		return "login.tailscale.com", "/", nil
	default:
		return "", "", errors.New("target must be tailscale_control or tailscale_login")
	}
}

var controlCGNAT = netip.MustParsePrefix("100.64.0.0/10")

func selectControlDNSIP(ips []net.IP, forced string) (net.IP, error) {
	var candidate net.IP
	if forced != "" {
		parsed, err := netip.ParseAddr(forced)
		if err != nil || !parsed.Is4() || parsed.Zone() != "" {
			return nil, errors.New("dial_ip must be one literal IPv4 address")
		}
		candidate = net.ParseIP(parsed.String()).To4()
	}
	var fallback net.IP
	for _, raw := range ips {
		ip := raw.To4()
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || controlCGNAT.Contains(netip.AddrFrom4([4]byte(ip))) {
			continue
		}
		if fallback == nil {
			fallback = ip
		}
		if candidate != nil && candidate.Equal(ip) {
			return ip, nil
		}
	}
	if candidate != nil {
		return nil, errors.New("dial_ip is not in the current verified DNS A answer")
	}
	if fallback == nil {
		return nil, errors.New("no admissible public DNS A address")
	}
	return fallback, nil
}

var lookupControlHTTPSIP = net.DefaultResolver.LookupIP

// ProbeControlHTTPS checks precisely two fixed HTTPS targets, not arbitrary
// host/path/script. TLS validates the original hostname even with a pinned
// destination address; proxies and redirects are disabled.
func ProbeControlHTTPS(ctx context.Context, target, dialIP string) (out ControlHTTPSProbe, err error) {
	host, requestPath, err := controlHTTPSHost(target)
	if err != nil {
		return out, err
	}
	out.Target, out.Host, out.Source = target, host, "H96"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	defer func() { out.TotalMillis = time.Since(start).Milliseconds() }()
	ips, lookupErr := lookupControlHTTPSIP(ctx, "ip4", host)
	out.DNSMillis = time.Since(start).Milliseconds()
	if lookupErr != nil {
		out.Stage, out.Error = "dns", "DNS lookup failed"
		return out, nil
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			out.DNSIPs = append(out.DNSIPs, ip.String())
		}
	}
	selected, selectErr := selectControlDNSIP(ips, strings.TrimSpace(dialIP))
	if selectErr != nil {
		out.Stage, out.Error = "dns", selectErr.Error()
		return out, nil
	}
	out.Dialed = net.JoinHostPort(selected.String(), "443")
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 4 * time.Second}
			return d.DialContext(ctx, "tcp", out.Dialed)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+requestPath, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("User-Agent", "mcpshka-control-probe")
	resp, requestErr := client.Do(req)
	if requestErr != nil {
		out.Stage = stageOf(requestErr)
		switch out.Stage {
		case "tls":
			out.Error = "TLS certificate or handshake failed"
		case "connect":
			out.Error = "TCP connection failed"
		case "timeout":
			out.Error = "HTTPS timeout"
		default:
			out.Error = "HTTPS request failed"
		}
		return out, nil
	}
	defer resp.Body.Close()
	out.StatusCode = resp.StatusCode
	if resp.TLS != nil {
		out.TLSValid = true
		out.TLSVersion = tls.VersionName(resp.TLS.Version)
	}
	out.OK = resp.StatusCode >= 200 && resp.StatusCode < 400 && out.TLSValid
	if !out.OK {
		out.Stage = "http"
		out.Error = "HTTPS response is not healthy"
	}
	return out, nil
}

var endpointResolvers = map[string]string{
	"tailnet": "100.100.100.100:53", // MagicDNS
	"public":  "1.1.1.1:53",         // how the internet sees a Funnel name
	"lan":     "",                   // the host's own resolver (dnsmasq)
}

// ProbeEndpoint resolves rawURL through via, dials the first address with the
// original name as SNI/Host and reports DNS, TLS and HTTP status separately.
func ProbeEndpoint(ctx context.Context, rawURL, via string) (out EndpointProbe, err error) {
	u, perr := url.Parse(strings.TrimSpace(rawURL))
	if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return EndpointProbe{}, errors.New("url must be http(s)://host[:port][/path] without credentials")
	}
	host := strings.ToLower(u.Hostname())
	if err := ownEndpoint(host); err != nil {
		return EndpointProbe{}, err
	}
	if via == "" {
		via = "lan"
		if strings.HasSuffix(host, ".ts.net") {
			via = "tailnet"
		}
	}
	server, known := endpointResolvers[via]
	if !known {
		return EndpointProbe{}, errors.New("via must be tailnet, public or lan")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	out = EndpointProbe{URL: u.String(), Via: via, Resolver: server}
	if server == "" {
		out.Resolver = "system"
	}
	ctx, cancel := context.WithTimeout(ctx, endpointTimeout)
	defer cancel()
	start := time.Now()
	defer func() { out.TotalMs = time.Since(start).Milliseconds() }()

	var addrs []string
	if via == "tailnet" {
		// The local resolver intercepts even 100.100.100.100:53 and answers
		// with the LAN address; tailscaled's own peer table is authoritative.
		addrs, err = tailnetLookup(ctx, host)
		if err == nil {
			out.Resolver = "tailscale status"
		}
	}
	if len(addrs) == 0 {
		addrs, err = resolveVia(ctx, host, server)
	}
	out.DNSMillis = time.Since(start).Milliseconds()
	if err != nil || len(addrs) == 0 {
		out.Stage, out.Error = "dns", fmt.Sprint("resolve: ", err)
		return out, nil
	}
	out.Addresses = addrs
	out.Dialed = net.JoinHostPort(addrs[0], port)

	dialer := &net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, out.Dialed)
		},
		TLSClientConfig:   &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		DisableKeepAlives: true,
		Proxy:             nil,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "mcpshka-endpoint-probe")
	resp, err := client.Do(req)
	if err != nil {
		out.Stage, out.Error = stageOf(err), err.Error()
		return out, nil
	}
	resp.Body.Close()
	out.StatusCode = resp.StatusCode
	if st := resp.TLS; st != nil {
		out.TLSValid = true // the handshake verified the chain for host
		out.TLSVersion = tls.VersionName(st.Version)
		if len(st.PeerCertificates) > 0 {
			out.CertExpiry = st.PeerCertificates[0].NotAfter.UTC()
		}
	}
	out.OK = resp.StatusCode < 500
	if !out.OK {
		out.Stage = "http"
	}
	return out, nil
}

// ownEndpoint keeps the probe on the owner's own addresses.
func ownEndpoint(host string) error {
	if strings.HasSuffix(host, ".ts.net") || host == "localhost" {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		cgnat := netip.MustParsePrefix("100.64.0.0/10")
		if ip.IsPrivate() || ip.IsLoopback() || cgnat.Contains(ip) || ip.IsLinkLocalUnicast() {
			return nil
		}
	}
	return errors.New("endpoint_probe checks only own endpoints: *.ts.net names or private/tailnet/loopback addresses")
}

var tailscaleStatus = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "/usr/bin/tailscale", "status", "--json").Output()
}

// tailnetLookup maps a MagicDNS name to its IPv4 tailnet address from
// tailscaled's own status (self and peers).
func tailnetLookup(ctx context.Context, host string) ([]string, error) {
	raw, err := tailscaleStatus(ctx)
	if err != nil {
		return nil, err
	}
	var st struct {
		Self *struct {
			DNSName      string
			TailscaleIPs []string
		}
		Peer map[string]struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	nodes := []struct {
		DNSName      string
		TailscaleIPs []string
	}{}
	if st.Self != nil {
		nodes = append(nodes, *st.Self)
	}
	for _, p := range st.Peer {
		nodes = append(nodes, p)
	}
	for _, n := range nodes {
		if strings.TrimSuffix(strings.ToLower(n.DNSName), ".") != host {
			continue
		}
		for _, ip := range n.TailscaleIPs {
			if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
				return []string{ip}, nil
			}
		}
	}
	return nil, errors.New("name not in tailnet status")
}

func resolveVia(ctx context.Context, host, server string) ([]string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []string{ip.String()}, nil
	}
	r := net.DefaultResolver
	if server != "" {
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server)
		}}
	}
	ips, err := r.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

func stageOf(err error) string {
	var cert *tls.CertificateVerificationError
	var opErr *net.OpError
	switch {
	case errors.As(err, &cert), strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "x509:"):
		return "tls"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "connect"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "http"
}
