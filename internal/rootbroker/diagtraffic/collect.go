package diagtraffic

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	code int
}

func launch(argv []string, stdin io.Reader, stdout, stderr io.Writer, limit int) (*process, error) {
	cmd := exec.Command("/usr/bin/prlimit", append([]string{"--fsize=" + strconv.Itoa(limit) + ":" + strconv.Itoa(limit), "--"}, argv...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		if err != nil {
			p.code = -1
			if cmd.ProcessState != nil {
				st := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if st.Signaled() {
					p.code = -int(st.Signal())
				} else {
					p.code = st.ExitStatus()
				}
			}
		}
		close(p.done)
	}()
	return p, nil
}

func (p *process) stopped() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *process) stop(first syscall.Signal, grace time.Duration) int {
	if !p.stopped() {
		// prlimit execs the collector in the same process.
		_ = p.cmd.Process.Signal(first)
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			_ = p.cmd.Process.Kill()
			select {
			case <-p.done:
			case <-time.After(time.Second):
				return -1
			}
		}
	}
	return p.code
}

type prefixBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *prefixBuffer) Write(p []byte) (int, error) {
	n := len(p)
	_, err := b.buffer.Write(p[:min(n, b.limit-b.buffer.Len())])
	return n, err
}

func collect(ctx context.Context, argv []string, stdin io.Reader, out io.Writer, limit int, timeout time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if _, err := os.Stat(argv[0]); err != nil {
		return -1, err
	}
	p, err := launch(argv, stdin, out, out, limit)
	if err != nil {
		return -1, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-p.done:
		return p.code, nil
	case <-ctx.Done():
		return p.stop(syscall.SIGTERM, 500*time.Millisecond), ctx.Err()
	}
}

func (d runDir) snapshot(ctx context.Context, name string, argv []string, stdin io.Reader, limit int, timeout time.Duration) string {
	f, err := d.create(name + ".txt")
	if err != nil {
		return name + ":" + err.Error()
	}
	defer f.Close()
	_, err = collect(ctx, argv, stdin, f, limit, timeout)
	if errors.Is(err, os.ErrNotExist) {
		return name + ":command_not_found"
	}
	if err != nil {
		return name + ":" + err.Error()
	}
	return ""
}

func conntrack(op string, ip netip.Addr, direction, format string) []string {
	argv := []string{"/usr/sbin/conntrack", op}
	if ip.Is6() {
		argv = append(argv, "-f", "ipv6")
	}
	return append(argv, direction, ip.String(), "-o", format)
}

type snapshotCommand struct {
	name string
	argv []string
}

func baseline(client netip.Addr, ipv6 []netip.Addr) []snapshotCommand {
	const ip = "/usr/sbin/ip"
	commands := []snapshotCommand{
		{"links", []string{ip, "-o", "link", "show"}},
		{"addr4", []string{ip, "-o", "-4", "addr", "show"}},
		{"addr6", []string{ip, "-o", "-6", "addr", "show"}},
		{"neigh4", []string{ip, "-4", "neigh", "show"}},
		{"neigh6", []string{ip, "-6", "neigh", "show"}},
		{"rules4", []string{ip, "-4", "rule", "show"}},
		{"rules6", []string{ip, "-6", "rule", "show"}},
		{"routes4", []string{ip, "-4", "route", "show", "table", "all"}},
		{"routes6", []string{ip, "-6", "route", "show", "table", "all"}},
		{"iptables", []string{"/usr/sbin/iptables-save", "-c"}},
		{"nft", []string{"/usr/sbin/nft", "list", "ruleset"}},
		{"ipset", []string{"/usr/sbin/ipset", "save"}},
		{"sockets", []string{"/usr/bin/ss", "-Htunap"}},
		{"conntrack_src", conntrack("-L", client, "-s", "extended")},
		{"conntrack_dst", conntrack("-L", client, "-d", "extended")},
		{"arp", []string{"/usr/bin/cat", "/proc/net/arp"}},
		{"netdev", []string{"/usr/bin/cat", "/proc/net/dev"}},
	}
	for i, addr := range ipv6 {
		commands = append(commands,
			snapshotCommand{fmt.Sprintf("conntrack6_src_%d", i), conntrack("-L", addr, "-s", "extended")},
			snapshotCommand{fmt.Sprintf("conntrack6_dst_%d", i), conntrack("-L", addr, "-d", "extended")})
	}
	if path := trustedTool("ip6tables-save"); path != "" {
		commands = append(commands, snapshotCommand{"ip6tables", []string{path, "-c"}})
	}
	for _, tool := range []struct{ bin, name string }{{"wg", "wireguard"}, {"awg", "amneziawg"}} {
		if path := trustedTool(tool.bin); path != "" {
			for _, field := range []string{"endpoints", "handshakes", "transfer"} {
				flag := field
				if flag == "handshakes" {
					flag = "latest-handshakes"
				}
				commands = append(commands, snapshotCommand{tool.name + "_" + field, []string{path, "show", "all", flag}})
			}
		}
	}
	return commands
}

func trustedTool(name string) string {
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin"} {
		if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return p
		}
	}
	return ""
}

func (d runDir) phase(ctx context.Context, label string, client netip.Addr, ipv6 []netip.Addr, errors *[]string) {
	for _, command := range baseline(client, ipv6) {
		if ctx.Err() != nil || (label == "pre" && d.stopRequested()) {
			return
		}
		if problem := d.snapshot(ctx, label+"."+command.name, command.argv, nil, snapshotLimit, 3*time.Second); problem != "" {
			*errors = append(*errors, problem)
		}
	}
}

func arpMAC(arp io.Reader, client string) string {
	lines := bufio.NewScanner(arp)
	for lines.Scan() {
		f := strings.Fields(lines.Text())
		if len(f) >= 6 && f[0] == client && strings.EqualFold(f[2], "0x2") && f[3] != "00:00:00:00:00:00" {
			return strings.ToLower(f[3])
		}
	}
	return ""
}

func readARP(client string) (string, error) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return arpMAC(f, client), nil
}

func neighborAddresses(raw []byte, mac string) []netip.Addr {
	var addresses []netip.Addr
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		f := strings.Fields(string(line))
		pos := slices.Index(f, "lladdr")
		if pos < 1 || pos+1 >= len(f) || !strings.EqualFold(f[pos+1], mac) {
			continue
		}
		if ip, err := netip.ParseAddr(f[0]); err == nil && ip.Is6() && !ip.IsMulticast() && !ip.IsUnspecified() {
			addresses = append(addresses, ip.WithZone(""))
		}
	}
	return uniqueIPv6(addresses)
}

func uniqueIPv6(addresses []netip.Addr) []netip.Addr {
	slices.SortFunc(addresses, func(a, b netip.Addr) int {
		if a.IsLinkLocalUnicast() != b.IsLinkLocalUnicast() {
			if a.IsLinkLocalUnicast() {
				return 1
			}
			return -1
		}
		return strings.Compare(a.String(), b.String())
	})
	addresses = slices.Compact(addresses)
	return addresses[:min(4, len(addresses))]
}

func discoverIPv6(ctx context.Context, mac string) ([]netip.Addr, string) {
	out := prefixBuffer{limit: snapshotLimit}
	rc, err := collect(ctx, []string{"/usr/sbin/ip", "-6", "neigh", "show"}, nil, &out, snapshotLimit, 2*time.Second)
	if err != nil {
		return nil, "ipv6_neigh:" + err.Error()
	}
	if rc != 0 {
		return nil, fmt.Sprintf("ipv6_neigh_rc=%d", rc)
	}
	return neighborAddresses(out.buffer.Bytes(), mac), ""
}

func appendSamples(ctx context.Context, sample *os.File, clients []netip.Addr, remaining int64) int64 {
	if remaining <= 0 {
		return 0
	}
	out := prefixBuffer{limit: int(remaining)}
	fmt.Fprintf(&out, "=== %s ===\n", now())
	for _, ip := range clients {
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(&out, "--- %s ---\n", ip)
		res := prefixBuffer{limit: 64 << 10}
		_, err := collect(ctx, conntrack("-L", ip, "-s", "extended"), nil, &res, snapshotLimit, time.Second)
		if err != nil {
			fmt.Fprintf(&out, "ERROR address=%s %v\n", ip, err)
		} else {
			out.Write(res.buffer.Bytes())
			if !bytes.HasSuffix(res.buffer.Bytes(), []byte{'\n'}) {
				out.Write([]byte{'\n'})
			}
		}
	}
	n, _ := sample.Write(out.buffer.Bytes())
	return int64(n)
}
