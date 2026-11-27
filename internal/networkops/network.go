package networkops

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	commandTimeout  = 15 * time.Second
	maxCommandBytes = 128 * 1024
	maxLinks        = 64
	maxRoutes       = 256
	maxSockets      = 256
	maxDNSBytes     = 64 * 1024
)

type Link struct {
	Name      string   `json:"name"`
	Index     int      `json:"index"`
	State     string   `json:"state,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

type Route struct {
	Family      string `json:"family,omitempty"`
	Destination string `json:"destination,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device,omitempty"`
	Metric      int    `json:"metric,omitempty"`
}

type DNS struct {
	Nameservers []string `json:"nameservers,omitempty"`
	Search      []string `json:"search,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type Socket struct {
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Local    string `json:"local"`
}

type VPN struct {
	Interface string `json:"interface"`
	Kind      string `json:"kind"`
	State     string `json:"state,omitempty"`
}

type Snapshot struct {
	CapturedAt  time.Time `json:"captured_at"`
	Links       []Link    `json:"links"`
	Routes      []Route   `json:"routes"`
	DNS         DNS       `json:"dns"`
	Sockets     []Socket  `json:"sockets"`
	VPNs        []VPN     `json:"vpns,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Truncated   bool      `json:"truncated"`
	Warnings    []string  `json:"warnings,omitempty"`
}

type StateDigest struct {
	CapturedAt    time.Time `json:"captured_at"`
	Fingerprint   string    `json:"fingerprint"`
	DefaultRoutes []Route   `json:"default_routes,omitempty"`
	Nameservers   []string  `json:"nameservers,omitempty"`
	VPNs          []VPN     `json:"vpns,omitempty"`
}

type ProbeResult struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type Connectivity struct {
	CapturedAt time.Time     `json:"captured_at"`
	OK         bool          `json:"ok"`
	Probes     []ProbeResult `json:"probes"`
}

type runner func(context.Context, string, int, ...string) ([]byte, bool, error)
type reader func(string, int) ([]byte, bool, error)

type Service struct {
	run          runner
	read         reader
	ipPath       string
	ssPath       string
	procFallback bool
	probe        func(context.Context) Connectivity
}

func New() *Service {
	return &Service{
		run:          runBounded,
		read:         readBounded,
		ipPath:       firstExisting("/usr/sbin/ip", "/usr/bin/ip", "/sbin/ip", "/bin/ip"),
		ssPath:       firstExisting("/usr/bin/ss", "/usr/sbin/ss", "/bin/ss", "/sbin/ss"),
		procFallback: true,
		probe:        fixedConnectivity,
	}
}

func newService(run runner, read reader, ipPath, ssPath string) *Service {
	return &Service{run: run, read: read, ipPath: ipPath, ssPath: ssPath}
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	out, err := s.snapshotCommands(ctx)
	if err == nil || !s.procFallback {
		if err == nil {
			out = finalizeSnapshot(out)
		}
		return out, err
	}
	fallback, fallbackErr := s.snapshotProc()
	if fallbackErr != nil {
		return Snapshot{}, fmt.Errorf("command snapshot failed: %v; proc/sysfs fallback failed: %w", err, fallbackErr)
	}
	fallback.Warnings = append(fallback.Warnings, "command snapshot unavailable; used proc/sysfs fallback")
	return finalizeSnapshot(fallback), nil
}

func (s *Service) Connectivity(ctx context.Context) (Connectivity, error) {
	if s == nil || s.probe == nil {
		return Connectivity{}, errors.New("connectivity probe is not configured")
	}
	return s.probe(ctx), nil
}

func Digest(snapshot Snapshot) StateDigest {
	out := StateDigest{
		CapturedAt:  snapshot.CapturedAt,
		Fingerprint: snapshot.Fingerprint,
		Nameservers: append([]string(nil), snapshot.DNS.Nameservers...),
		VPNs:        append([]VPN(nil), snapshot.VPNs...),
	}
	for _, route := range snapshot.Routes {
		if route.Destination == "default" && len(out.DefaultRoutes) < 8 {
			out.DefaultRoutes = append(out.DefaultRoutes, route)
		}
	}
	return out
}

func (s *Service) snapshotCommands(ctx context.Context) (Snapshot, error) {
	out := Snapshot{CapturedAt: time.Now().UTC()}
	if s.ipPath == "" {
		return Snapshot{}, errors.New("ip command not found")
	}
	if s.ssPath == "" {
		return Snapshot{}, errors.New("ss command not found")
	}

	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	if raw, truncated, err := s.run(child, s.ipPath, maxCommandBytes, "-j", "-d", "address", "show"); err != nil {
		return Snapshot{}, fmt.Errorf("ip address: %w: %s", err, bounded(raw))
	} else if truncated {
		out.Truncated = true
		out.Warnings = append(out.Warnings, "ip address output truncated")
	} else if links, err := parseLinks(raw); err != nil {
		return Snapshot{}, err
	} else {
		if len(links) > maxLinks {
			links = links[:maxLinks]
			out.Truncated = true
		}
		out.Links = links
	}

	if raw, truncated, err := s.run(child, s.ipPath, maxCommandBytes, "-j", "route", "show", "table", "all"); err != nil {
		out.Warnings = append(out.Warnings, "routes: "+err.Error())
	} else if truncated {
		out.Truncated = true
		out.Warnings = append(out.Warnings, "route output truncated")
	} else if routes, err := parseRoutes(raw); err != nil {
		out.Warnings = append(out.Warnings, "routes: "+err.Error())
	} else {
		if len(routes) > maxRoutes {
			routes = routes[:maxRoutes]
			out.Truncated = true
		}
		out.Routes = routes
	}

	if raw, truncated, err := s.read("/etc/resolv.conf", maxDNSBytes); err != nil {
		out.Warnings = append(out.Warnings, "dns: "+err.Error())
	} else {
		out.DNS = parseDNS(raw)
		out.Truncated = out.Truncated || truncated
	}

	if raw, truncated, err := s.run(child, s.ssPath, maxCommandBytes, "-H", "-lntu"); err != nil {
		out.Warnings = append(out.Warnings, "sockets: "+err.Error())
	} else {
		sockets := parseSockets(raw)
		if len(sockets) > maxSockets {
			sockets = sockets[:maxSockets]
			truncated = true
		}
		out.Sockets = sockets
		out.Truncated = out.Truncated || truncated
	}

	return out, nil
}

func parseLinks(raw []byte) ([]Link, error) {
	var rows []struct {
		IfIndex   int    `json:"ifindex"`
		IfName    string `json:"ifname"`
		OperState string `json:"operstate"`
		MTU       int    `json:"mtu"`
		LinkInfo  struct {
			InfoKind string `json:"info_kind"`
		} `json:"linkinfo"`
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode ip address: %w", err)
	}
	out := make([]Link, 0, len(rows))
	for _, row := range rows {
		if row.IfName == "" {
			continue
		}
		item := Link{Name: row.IfName, Index: row.IfIndex, State: strings.ToLower(row.OperState), Kind: strings.ToLower(row.LinkInfo.InfoKind), MTU: row.MTU}
		for _, addr := range row.AddrInfo {
			if net.ParseIP(addr.Local) == nil || len(item.Addresses) >= 16 {
				continue
			}
			item.Addresses = append(item.Addresses, addr.Local+"/"+strconv.Itoa(addr.PrefixLen))
		}
		out = append(out, item)
	}
	return out, nil
}

func parseRoutes(raw []byte) ([]Route, error) {
	var rows []struct {
		Dst     string `json:"dst"`
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
		Metric  int    `json:"metric"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode routes: %w", err)
	}
	out := make([]Route, 0, len(rows))
	for _, row := range rows {
		dst := row.Dst
		if dst == "" {
			dst = "default"
		}
		family := "ipv4"
		check := strings.TrimSuffix(dst, "/0")
		if row.Gateway != "" {
			check = row.Gateway
		}
		if ip := net.ParseIP(check); ip != nil && ip.To4() == nil {
			family = "ipv6"
		} else if strings.Contains(dst, ":") {
			family = "ipv6"
		}
		out = append(out, Route{
			Family: family, Destination: dst, Gateway: row.Gateway, Device: row.Dev, Metric: row.Metric,
		})
	}
	return out, nil
}

func parseDNS(raw []byte) DNS {
	var out DNS
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			if len(out.Nameservers) < 8 && net.ParseIP(fields[1]) != nil {
				out.Nameservers = append(out.Nameservers, fields[1])
			}
		case "search", "domain":
			for _, value := range fields[1:] {
				if len(out.Search) >= 16 {
					break
				}
				out.Search = append(out.Search, value)
			}
		case "options":
			for _, value := range fields[1:] {
				if len(out.Options) >= 16 {
					break
				}
				out.Options = append(out.Options, value)
			}
		}
	}
	return out
}

func parseSockets(raw []byte) []Socket {
	var out []Socket
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		out = append(out, Socket{
			Protocol: fields[0],
			State:    strings.ToLower(fields[1]),
			Local:    fields[4],
		})
	}
	return out
}

func finalizeSnapshot(out Snapshot) Snapshot {
	out.VPNs = detectVPNs(out.Links)
	links := append([]Link(nil), out.Links...)
	routes := append([]Route(nil), out.Routes...)
	sort.Slice(links, func(i, j int) bool { return links[i].Name < links[j].Name })
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Family != routes[j].Family {
			return routes[i].Family < routes[j].Family
		}
		if routes[i].Destination != routes[j].Destination {
			return routes[i].Destination < routes[j].Destination
		}
		if routes[i].Device != routes[j].Device {
			return routes[i].Device < routes[j].Device
		}
		if routes[i].Gateway != routes[j].Gateway {
			return routes[i].Gateway < routes[j].Gateway
		}
		return routes[i].Metric < routes[j].Metric
	})
	canonical := struct {
		Links  []Link  `json:"links"`
		Routes []Route `json:"routes"`
		DNS    DNS     `json:"dns"`
		VPNs   []VPN   `json:"vpns"`
	}{links, routes, out.DNS, out.VPNs}
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	out.Fingerprint = fmt.Sprintf("%x", sum[:])
	return out
}

func detectVPNs(links []Link) []VPN {
	out := make([]VPN, 0)
	for _, link := range links {
		name := strings.ToLower(link.Name)
		kind := strings.ToLower(link.Kind)
		vpnKind := ""
		switch {
		case kind == "amneziawg" || strings.HasPrefix(name, "awg"):
			vpnKind = "amneziawg"
		case kind == "wireguard" || strings.HasPrefix(name, "wg"):
			vpnKind = "wireguard"
		case strings.HasPrefix(name, "tailscale"):
			vpnKind = "tailscale"
		case kind == "tun" || kind == "tap" || strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "ovpn"):
			vpnKind = "tunnel"
		}
		if vpnKind == "" {
			continue
		}
		out = append(out, VPN{Interface: link.Name, Kind: vpnKind, State: link.State})
		if len(out) >= 32 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Interface < out[j].Interface })
	return out
}

func fixedConnectivity(parent context.Context) Connectivity {
	out := Connectivity{CapturedAt: time.Now().UTC()}

	dnsStart := time.Now()
	dnsCtx, dnsCancel := context.WithTimeout(parent, 4*time.Second)
	hosts, dnsErr := net.DefaultResolver.LookupHost(dnsCtx, "one.one.one.one")
	dnsCancel()
	dnsProbe := ProbeResult{Name: "dns", LatencyMS: elapsedMillis(dnsStart)}
	if dnsErr == nil && len(hosts) > 0 {
		dnsProbe.OK = true
		dnsProbe.Detail = fmt.Sprintf("resolved=%d", len(hosts))
	} else if dnsErr != nil {
		dnsProbe.Detail = compactProbeError(dnsErr)
	} else {
		dnsProbe.Detail = "no addresses"
	}
	out.Probes = append(out.Probes, dnsProbe)

	httpStart := time.Now()
	httpCtx, httpCancel := context.WithTimeout(parent, 5*time.Second)
	req, reqErr := http.NewRequestWithContext(httpCtx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	httpProbe := ProbeResult{Name: "https", LatencyMS: elapsedMillis(httpStart)}
	if reqErr != nil {
		httpProbe.Detail = compactProbeError(reqErr)
	} else {
		client := &http.Client{
			Timeout:       5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, err := client.Do(req)
		httpProbe.LatencyMS = elapsedMillis(httpStart)
		if err != nil {
			httpProbe.Detail = compactProbeError(err)
		} else {
			httpProbe.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
			httpProbe.Detail = fmt.Sprintf("http=%d", resp.StatusCode)
			_ = resp.Body.Close()
		}
	}
	httpCancel()
	out.Probes = append(out.Probes, httpProbe)
	out.OK = dnsProbe.OK && httpProbe.OK
	return out
}

func elapsedMillis(start time.Time) int64 {
	ms := time.Since(start).Milliseconds()
	if ms < 1 {
		return 1
	}
	return ms
}

func compactProbeError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 256 {
		text = text[:256]
	}
	return text
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

type cappedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		b.buf = append(b.buf, p[:keep]...)
	}
	if remaining <= 0 || len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func runBounded(ctx context.Context, path string, maxBytes int, args ...string) ([]byte, bool, error) {
	buf := &cappedBuffer{limit: maxBytes}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	return append([]byte(nil), buf.buf...), buf.truncated, err
}

func readBounded(path string, maxBytes int) ([]byte, bool, error) {
	if maxBytes <= 0 {
		return nil, false, errors.New("invalid byte limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	buf := make([]byte, maxBytes+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, false, err
	}
	truncated := n > maxBytes
	if truncated {
		n = maxBytes
	}
	return buf[:n], truncated, nil
}

func bounded(data []byte) string {
	if len(data) > 2048 {
		data = data[:2048]
	}
	return strings.TrimSpace(string(data))
}
