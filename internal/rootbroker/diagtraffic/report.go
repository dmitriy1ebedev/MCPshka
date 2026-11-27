package diagtraffic

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	flowLine   = regexp.MustCompile(`(?:^|\s)(IP6|IP)\s+(\S+)\s+>\s+(\S+)`)
	sll2Header = regexp.MustCompile(`^\S+\s+\S+\s+([A-Za-z0-9_.:-]+)\s+(?:In|Out)\s+.*ethertype\s+(IPv6|IPv4)\s`)
	sll2Flow   = regexp.MustCompile(`^\s+(\S+)\s+>\s+(\S+)`)
	dnsQuery   = regexp.MustCompile(`\b(?:A|AAAA)\?\s+([A-Za-z0-9_.-]+)\.`)
	mark       = regexp.MustCompile(`\bmark=(0x[0-9a-fA-F]+|\d+)\b`)
	iface      = regexp.MustCompile(`^(?:\S+\s+){1,2}([A-Za-z0-9_.:-]+)\s+(?:In|Out)\s+`)
	portNumber = regexp.MustCompile(`^[0-9]+$`)
)

type pcapFile struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type count struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type report struct {
	Schema      string     `json:"schemaVersion"`
	RunID       string     `json:"run_id"`
	Client      string     `json:"client"`
	IPv6        []string   `json:"client_ipv6"`
	Packets     int        `json:"packet_lines_parsed"`
	IPv4Packets int        `json:"ipv4_packet_lines"`
	IPv6Packets int        `json:"ipv6_packet_lines"`
	QUIC        int        `json:"quic_candidate_packets"`
	Protocols   []count    `json:"protocols"`
	Remote      []count    `json:"top_remote_ips"`
	RemoteIPv6  []count    `json:"top_remote_ipv6"`
	Ports       []count    `json:"top_remote_ports"`
	DNS         []count    `json:"dns_queries"`
	Interfaces  []count    `json:"observed_interfaces"`
	Marks       []count    `json:"conntrack_marks"`
	PCAPs       []pcapFile `json:"pcap_files"`
	Notes       []string   `json:"notes"`
}

func (d runDir) names() []string {
	f, err := d.fs.Open(".")
	if err != nil {
		return nil
	}
	defer f.Close()
	names, _ := f.Readdirnames(256)
	slices.Sort(names)
	return names
}

func (d runDir) pcaps() []pcapFile {
	files := []pcapFile{}
	for _, name := range d.names() {
		if !strings.HasPrefix(name, "traffic.pcap") {
			continue
		}
		info, err := d.fs.Lstat(name)
		if err == nil && info.Mode().IsRegular() && info.Size() >= 24 && info.Size() <= pcapCap {
			files = append(files, pcapFile{name, info.Size()})
		}
	}
	return files
}

func pcapBytes(files []pcapFile) int64 {
	var n int64
	for _, p := range files {
		n += p.Bytes
	}
	return n
}

func (d runDir) render(ctx context.Context, i int, pcap io.Reader) string {
	return d.snapshot(ctx, fmt.Sprintf("pcap_text_%d", i),
		[]string{"/usr/bin/tcpdump", "-nn", "-tttt", "-e", "-vv", "-r", "-"}, pcap, textLimit, 8*time.Second)
}

func splitEndpoint(token string) (netip.Addr, string) {
	token = strings.TrimRight(token, ":,")
	if ip, err := netip.ParseAddr(token); err == nil {
		return ip, ""
	}
	i := strings.LastIndexByte(token, '.')
	if i < 0 || !portNumber.MatchString(token[i+1:]) {
		return netip.Addr{}, ""
	}
	ip, _ := netip.ParseAddr(token[:i])
	return ip, token[i+1:]
}

func lineProtocol(line string) string {
	switch {
	case strings.Contains(line, " UDP, "):
		return "udp"
	case strings.Contains(line, " Flags ["):
		return "tcp"
	case strings.Contains(line, " ICMP6"):
		return "icmp6"
	case strings.Contains(line, " ICMP "):
		return "icmp"
	default:
		return "other"
	}
}

func top(data map[string]int) []count {
	values := make([]count, 0, len(data))
	for k, n := range data {
		values = append(values, count{k, n})
	}
	slices.SortFunc(values, func(a, b count) int {
		if n := cmp.Compare(b.Count, a.Count); n != 0 {
			return n
		}
		return cmp.Compare(a.Value, b.Value)
	})
	return values[:min(30, len(values))]
}

func stringsOfIPs(ips []netip.Addr) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func (d runDir) summarize(client netip.Addr, ipv6 []netip.Addr, files []pcapFile) report {
	clients := append([]netip.Addr{client}, ipv6...)
	r := report{Schema: schema, RunID: d.id, Client: client.String(), IPv6: stringsOfIPs(ipv6), PCAPs: files,
		Notes: []string{
			"The run is passive: no route/firewall/DNS/DHCP/service mutation is performed.",
			"IPv6 coverage uses the bounded capture and maps pre/post neighbor identities back to the expected client MAC.",
			"UDP/443 is reported as a QUIC candidate; encrypted application payload is intentionally not decrypted.",
			"A mark is kernel classification evidence; actual path must be cross-checked against captured interface plus saved rules/routes.",
		}}
	remotes, remotes6, ports, domains := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	interfaces, protocols, marks := map[string]int{}, map[string]int{}, map[string]int{}
	for _, name := range d.names() {
		switch {
		case strings.HasPrefix(name, "pcap_text_") && strings.HasSuffix(name, ".txt"):
			raw, err := d.readFile(name, textLimit)
			if err != nil {
				continue
			}
			lines := bufio.NewScanner(bytes.NewReader(raw))
			lines.Buffer(make([]byte, 4096), 256<<10)
			pendingFamily, pendingInterface := "", ""
			for lines.Scan() {
				line := lines.Text()
				if header := sll2Header.FindStringSubmatch(line); header != nil {
					pendingFamily, pendingInterface = header[2], header[1]
					continue
				}
				if family, intf := splitHeader(line); family != "" {
					pendingFamily, pendingInterface = family, intf
					continue
				}
				var family, intf, srcToken, dstToken string
				if f := flowLine.FindStringSubmatch(line); f != nil {
					family, srcToken, dstToken = f[1], f[2], f[3]
				} else if f := sll2Flow.FindStringSubmatch(line); f != nil && pendingFamily != "" {
					family, intf, srcToken, dstToken = pendingFamily, pendingInterface, f[1], f[2]
				}
				pendingFamily, pendingInterface = "", ""
				src, srcPort := splitEndpoint(srcToken)
				dst, dstPort := splitEndpoint(dstToken)
				if !src.IsValid() || !dst.IsValid() {
					continue
				}
				remote, port := dst, dstPort
				if !slices.Contains(clients, src) {
					if !slices.Contains(clients, dst) {
						continue
					}
					remote, port = src, srcPort
				}
				r.Packets++
				if family == "IP6" || family == "IPv6" {
					r.IPv6Packets++
				} else {
					r.IPv4Packets++
				}
				if intf == "" {
					if match := iface.FindStringSubmatch(line); match != nil {
						intf = match[1]
					}
				}
				if intf != "" {
					interfaces[intf]++
				}
				proto := lineProtocol(line)
				protocols[proto]++
				remotes[remote.String()]++
				if remote.Is6() {
					remotes6[remote.String()]++
				}
				if port != "" {
					ports[port]++
				}
				if proto == "udp" && port == "443" {
					r.QUIC++
				}
				for _, m := range dnsQuery.FindAllStringSubmatch(line, -1) {
					domains[strings.ToLower(m[1])]++
				}
			}
		case name == "conntrack.samples.log" || (strings.HasPrefix(name, "conntrack.events") && strings.HasSuffix(name, ".log")):
			raw, err := d.readFile(name, textLimit)
			if err == nil {
				for _, m := range mark.FindAllSubmatch(raw, -1) {
					marks[string(m[1])]++
				}
			}
		}
	}
	r.Protocols, r.Remote, r.RemoteIPv6, r.Ports = top(protocols), top(remotes), top(remotes6), top(ports)
	r.DNS, r.Interfaces, r.Marks = top(domains), top(interfaces), top(marks)
	return r
}

func splitHeader(line string) (string, string) {
	family := ""
	if strings.Contains(line, " ethertype IPv4 ") {
		family = "IPv4"
	} else if strings.Contains(line, " ethertype IPv6 ") {
		family = "IPv6"
	}
	if family != "" {
		f := strings.Fields(line)
		if len(f) >= 4 && (f[3] == "In" || f[3] == "Out") {
			return family, f[2]
		}
	}
	return "", ""
}

func (d runDir) writeReport(r report) error {
	if err := d.writeJSON("report.json", r); err != nil {
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "DIAG traffic route %s\nclient=%s packets=%d ipv4=%d ipv6=%d quic_candidates=%d pcap_files=%d\n",
		r.RunID, r.Client, r.Packets, r.IPv4Packets, r.IPv6Packets, r.QUIC, len(r.PCAPs))
	for _, row := range []struct {
		label  string
		values []count
	}{
		{"interfaces", r.Interfaces}, {"marks", r.Marks}, {"protocols", r.Protocols},
		{"remote_ips", r.Remote}, {"remote_ipv6", r.RemoteIPv6}, {"remote_ports", r.Ports}, {"dns", r.DNS},
	} {
		items := []string{}
		for _, c := range row.values[:min(10, len(row.values))] {
			items = append(items, fmt.Sprintf("%s(%d)", c.Value, c.Count))
		}
		value := strings.Join(items, ", ")
		if value == "" {
			value = "-"
		}
		fmt.Fprintf(&text, "%s: %s\n", row.label, value)
	}
	return d.writeFile("summary.txt", []byte(text.String()))
}
