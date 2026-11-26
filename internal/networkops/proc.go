package networkops

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	procRoot      = "/proc/net"
	sysNetRoot    = "/sys/class/net"
	maxProcBytes  = 512 * 1024
	maxSmallBytes = 4096
)

func (s *Service) snapshotProc() (Snapshot, error) {
	out := Snapshot{CapturedAt: time.Now().UTC()}
	links, truncated, err := readProcLinks()
	if err != nil {
		return Snapshot{}, err
	}
	out.Links = links
	out.Truncated = truncated

	routes, matches, truncated, err := readProcIPv4Routes()
	if err != nil {
		out.Warnings = append(out.Warnings, "ipv4 routes: "+err.Error())
	} else {
		out.Routes = append(out.Routes, routes...)
		out.Truncated = out.Truncated || truncated
	}
	if routes, truncated, err := readProcIPv6Routes(); err != nil {
		if !os.IsNotExist(err) {
			out.Warnings = append(out.Warnings, "ipv6 routes: "+err.Error())
		}
	} else {
		out.Routes = append(out.Routes, routes...)
		out.Truncated = out.Truncated || truncated
	}
	if locals, truncated, err := readProcLocalIPv4(); err != nil {
		if !os.IsNotExist(err) {
			out.Warnings = append(out.Warnings, "ipv4 addresses: "+err.Error())
		}
	} else {
		assignProcIPv4(&out, locals, matches)
		out.Truncated = out.Truncated || truncated
	}
	if truncated, err := addProcIPv6(&out); err != nil {
		if !os.IsNotExist(err) {
			out.Warnings = append(out.Warnings, "ipv6 addresses: "+err.Error())
		}
	} else {
		out.Truncated = out.Truncated || truncated
	}
	if raw, truncated, err := s.read("/etc/resolv.conf", maxDNSBytes); err != nil {
		out.Warnings = append(out.Warnings, "dns: "+err.Error())
	} else {
		out.DNS = parseDNS(raw)
		out.Truncated = out.Truncated || truncated
	}
	sockets, truncated, warnings := readProcSockets()
	out.Sockets = sockets
	out.Truncated = out.Truncated || truncated
	out.Warnings = append(out.Warnings, warnings...)
	sort.Slice(out.Routes, func(i, j int) bool {
		a, b := out.Routes[i], out.Routes[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.Destination != b.Destination {
			return a.Destination < b.Destination
		}
		return a.Device < b.Device
	})
	sort.Slice(out.Sockets, func(i, j int) bool {
		if out.Sockets[i].Protocol != out.Sockets[j].Protocol {
			return out.Sockets[i].Protocol < out.Sockets[j].Protocol
		}
		return out.Sockets[i].Local < out.Sockets[j].Local
	})
	return out, nil
}

func readProcLinks() ([]Link, bool, error) {
	entries, err := os.ReadDir(sysNetRoot)
	if err != nil {
		return nil, false, fmt.Errorf("read links: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	truncated := false
	if len(entries) > maxLinks {
		entries = entries[:maxLinks]
		truncated = true
	}
	out := make([]Link, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !safeIfName(name) {
			continue
		}
		link := Link{Name: name}
		if v, err := readProcText(filepath.Join(sysNetRoot, name, "ifindex")); err == nil {
			link.Index, _ = strconv.Atoi(v)
		}
		if v, err := readProcText(filepath.Join(sysNetRoot, name, "operstate")); err == nil {
			link.State = strings.ToLower(v)
		}
		if v, err := readProcText(filepath.Join(sysNetRoot, name, "mtu")); err == nil {
			link.MTU, _ = strconv.Atoi(v)
		}
		out = append(out, link)
	}
	return out, truncated, nil
}

type procRouteMatch struct {
	device  string
	network *net.IPNet
	prefix  int
}

func readProcIPv4Routes() ([]Route, []procRouteMatch, bool, error) {
	raw, truncated, err := readBounded(filepath.Join(procRoot, "route"), maxProcBytes)
	if err != nil {
		return nil, nil, false, err
	}
	var out []Route
	var matches []procRouteMatch
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(scanner.Text())
		if len(f) < 8 || !safeIfName(f[0]) {
			continue
		}
		dst, e1 := decodeProcIPv4(f[1])
		gw, e2 := decodeProcIPv4(f[2])
		maskIP, e3 := decodeProcIPv4(f[7])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		mask := net.IPMask(maskIP.To4())
		ones, _ := mask.Size()
		network := &net.IPNet{IP: dst.Mask(mask), Mask: mask}
		destination := network.String()
		if dst.Equal(net.IPv4zero) && ones == 0 {
			destination = "default"
		}
		gateway := ""
		if !gw.Equal(net.IPv4zero) {
			gateway = gw.String()
		}
		metric, _ := strconv.Atoi(f[6])
		if len(out) < maxRoutes {
			out = append(out, Route{Family: "ipv4", Destination: destination, Gateway: gateway, Device: f[0], Metric: metric})
		} else {
			truncated = true
		}
		if gw.Equal(net.IPv4zero) && ones > 0 {
			matches = append(matches, procRouteMatch{device: f[0], network: network, prefix: ones})
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].prefix > matches[j].prefix })
	return out, matches, truncated, scanner.Err()
}

func readProcIPv6Routes() ([]Route, bool, error) {
	raw, truncated, err := readBounded(filepath.Join(procRoot, "ipv6_route"), maxProcBytes)
	if err != nil {
		return nil, false, err
	}
	var out []Route
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 10 || !safeIfName(f[9]) {
			continue
		}
		dst, e1 := decodeIPv6Hex(f[0])
		prefix, e2 := strconv.ParseUint(f[1], 16, 8)
		gw, e3 := decodeIPv6Hex(f[4])
		metric, _ := strconv.ParseUint(f[5], 16, 32)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		destination := fmt.Sprintf("%s/%d", dst.String(), prefix)
		if prefix == 0 && dst.IsUnspecified() {
			destination = "default"
		}
		gateway := ""
		if !gw.IsUnspecified() {
			gateway = gw.String()
		}
		if len(out) >= maxRoutes {
			truncated = true
			continue
		}
		out = append(out, Route{Family: "ipv6", Destination: destination, Gateway: gateway, Device: f[9], Metric: int(metric)})
	}
	return out, truncated, scanner.Err()
}

func readProcLocalIPv4() ([]net.IP, bool, error) {
	raw, truncated, err := readBounded(filepath.Join(procRoot, "fib_trie"), maxProcBytes)
	if err != nil {
		return nil, false, err
	}
	seen := map[string]struct{}{}
	var out []net.IP
	var candidate net.IP
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "|-- ") || strings.HasPrefix(line, "+-- ") {
			candidate = net.ParseIP(strings.TrimSpace(line[4:])).To4()
			continue
		}
		if candidate != nil && strings.Contains(line, "host LOCAL") {
			key := candidate.String()
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				out = append(out, append(net.IP(nil), candidate...))
			}
			candidate = nil
		}
	}
	sort.Slice(out, func(i, j int) bool { return procIPLess(out[i], out[j]) })
	return out, truncated, scanner.Err()
}

func assignProcIPv4(out *Snapshot, locals []net.IP, matches []procRouteMatch) {
	index := map[string]int{}
	for i := range out.Links {
		index[out.Links[i].Name] = i
	}
	for _, ip := range locals {
		device := ""
		if ip[0] == 127 {
			device = "lo"
		} else {
			for _, match := range matches {
				if match.network.Contains(ip) {
					device = match.device
					break
				}
			}
		}
		if pos, ok := index[device]; ok && len(out.Links[pos].Addresses) < 24 {
			procAppendUnique(&out.Links[pos].Addresses, ip.String())
		}
	}
}

func addProcIPv6(out *Snapshot) (bool, error) {
	raw, truncated, err := readBounded(filepath.Join(procRoot, "if_inet6"), maxProcBytes)
	if err != nil {
		return false, err
	}
	index := map[string]int{}
	for i := range out.Links {
		index[out.Links[i].Name] = i
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 6 || !safeIfName(f[5]) {
			continue
		}
		ip, e1 := decodeIPv6Hex(f[0])
		prefix, e2 := strconv.ParseUint(f[2], 16, 8)
		if e1 != nil || e2 != nil {
			continue
		}
		if pos, ok := index[f[5]]; ok && len(out.Links[pos].Addresses) < 24 {
			procAppendUnique(&out.Links[pos].Addresses, fmt.Sprintf("%s/%d", ip.String(), prefix))
		}
	}
	return truncated, scanner.Err()
}

func readProcSockets() ([]Socket, bool, []string) {
	specs := []struct {
		file, proto string
		v6, tcp     bool
	}{
		{"tcp", "tcp", false, true},
		{"tcp6", "tcp6", true, true},
		{"udp", "udp", false, false},
		{"udp6", "udp6", true, false},
	}
	var out []Socket
	var warnings []string
	truncated := false
	for _, spec := range specs {
		raw, fileTruncated, err := readBounded(filepath.Join(procRoot, spec.file), maxProcBytes)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, spec.file+": "+err.Error())
			}
			continue
		}
		truncated = truncated || fileTruncated
		rows, err := parseProcSocketRows(raw, spec.proto, spec.v6, spec.tcp)
		if err != nil {
			warnings = append(warnings, spec.file+": "+err.Error())
			continue
		}
		for _, row := range rows {
			if len(out) >= maxSockets {
				truncated = true
				break
			}
			out = append(out, row)
		}
	}
	return out, truncated, warnings
}

func parseProcSocketRows(raw []byte, proto string, ipv6, tcp bool) ([]Socket, error) {
	var out []Socket
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(scanner.Text())
		if len(f) < 4 {
			continue
		}
		state := strings.ToUpper(f[3])
		if tcp && state != "0A" {
			continue
		}
		if !tcp && state != "07" {
			continue
		}
		local, err := decodeProcEndpoint(f[1], ipv6)
		if err != nil {
			continue
		}
		label := "unconn"
		if tcp {
			label = "listen"
		}
		out = append(out, Socket{Protocol: proto, State: label, Local: local})
	}
	return out, scanner.Err()
}

func decodeProcEndpoint(value string, ipv6 bool) (string, error) {
	addrHex, portHex, ok := strings.Cut(value, ":")
	if !ok {
		return "", fmt.Errorf("invalid proc endpoint")
	}
	var ip net.IP
	var err error
	if ipv6 {
		ip, err = decodeProcIPv6(addrHex)
	} else {
		ip, err = decodeProcIPv4(addrHex)
	}
	if err != nil {
		return "", err
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(port))), nil
}

func decodeProcIPv4(value string) (net.IP, error) {
	if len(value) != 8 {
		return nil, fmt.Errorf("invalid ipv4")
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 4 {
		return nil, fmt.Errorf("invalid ipv4")
	}
	return net.IPv4(raw[3], raw[2], raw[1], raw[0]), nil
}

func decodeProcIPv6(value string) (net.IP, error) {
	if len(value) != 32 {
		return nil, fmt.Errorf("invalid ipv6")
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("invalid ipv6")
	}
	for i := 0; i < 16; i += 4 {
		raw[i], raw[i+3] = raw[i+3], raw[i]
		raw[i+1], raw[i+2] = raw[i+2], raw[i+1]
	}
	return net.IP(raw), nil
}

func decodeIPv6Hex(value string) (net.IP, error) {
	if len(value) != 32 {
		return nil, fmt.Errorf("invalid ipv6")
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("invalid ipv6")
	}
	return net.IP(raw), nil
}

func readProcText(path string) (string, error) {
	raw, _, err := readBounded(path, maxSmallBytes)
	return strings.TrimSpace(string(raw)), err
}

func safeIfName(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' || r == ':' || r == '@' {
			continue
		}
		return false
	}
	return true
}

func procAppendUnique(dst *[]string, value string) {
	for _, existing := range *dst {
		if existing == value {
			return
		}
	}
	*dst = append(*dst, value)
}

func procIPLess(a, b net.IP) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
