package dockerapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LANMDNS is a host observation, never permission to restart a container.
type LANMDNS struct {
	LANIP     string
	Bound     bool
	StartedAt string
	PID       int
	Error     string
}

// mdns inspects only go2rtc; errors remain UNKNOWN instead of a missing socket.
func mdns(ctx context.Context) LANMDNS {
	out, err := observeMDNS(ctx)
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

func observeMDNS(ctx context.Context) (LANMDNS, error) {
	var out LANMDNS
	raw, err := Get(ctx, "/containers/go2rtc/json")
	if err != nil {
		return out, err
	}
	var info struct {
		State struct {
			Running, Restarting bool
			Pid                 int
			StartedAt           string
		}
		HostConfig struct{ NetworkMode string }
	}
	if err = json.Unmarshal(raw, &info); err != nil {
		return out, err
	}
	out.StartedAt, out.PID = info.State.StartedAt, info.State.Pid
	if !info.State.Running || info.State.Restarting || info.HostConfig.NetworkMode != "host" {
		return out, nil
	}
	routes, err := mdnsRead("/proc/net/route")
	if err != nil {
		return out, err
	}
	device, err := defaultDevice(string(routes))
	if err != nil || device == "" {
		return out, err
	}
	iface, err := net.InterfaceByName(device)
	if err != nil {
		return out, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return out, err
	}
	for _, addr := range addrs {
		ip, _, e := net.ParseCIDR(addr.String())
		if e == nil && ip.To4() != nil && ip.IsPrivate() {
			out.LANIP = ip.String()
			break
		}
	}
	if out.LANIP == "" {
		return out, nil
	}
	out.Bound, err = processMDNS("/proc", out.PID, net.ParseIP(out.LANIP))
	return out, err
}

func defaultDevice(raw string) (string, error) {
	device, metric := "", uint64(^uint32(0))
	for i, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 8 {
			return "", errors.New("invalid route row")
		}
		if f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, e1 := strconv.ParseUint(f[3], 16, 32)
		m, e2 := strconv.ParseUint(f[6], 10, 32)
		if e1 != nil || e2 != nil {
			return "", errors.New("invalid default route")
		}
		if flags&1 != 0 && m < metric {
			device, metric = f[0], m
		}
	}
	return device, nil
}

func mdnsRead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if len(raw) > 256<<10 {
		return nil, errors.New("mDNS observation exceeds byte limit")
	}
	return raw, err
}

func processMDNS(root string, pid int, ip net.IP) (bool, error) {
	if pid <= 0 || ip.To4() == nil {
		return false, errors.New("invalid go2rtc process identity")
	}
	dir := filepath.Join(root, strconv.Itoa(pid))
	comm, err := mdnsRead(filepath.Join(dir, "comm"))
	if err != nil || strings.TrimSpace(string(comm)) != "go2rtc" {
		return false, errors.New("go2rtc process identity unavailable")
	}
	fd, err := os.Open(filepath.Join(dir, "fd"))
	if err != nil {
		return false, err
	}
	defer fd.Close()
	entries, err := fd.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if len(entries) > 4096 {
		return false, errors.New("go2rtc fd limit exceeded")
	}
	inodes := map[string]bool{}
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join(dir, "fd", entry.Name()))
		if err != nil {
			return false, err
		}
		if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	raw, err := mdnsRead(filepath.Join(root, "net", "udp"))
	if err != nil {
		return false, err
	}
	endpoint := fmt.Sprintf("%08X:14E9", binary.NativeEndian.Uint32(ip.To4()))
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 10 {
			return false, errors.New("invalid UDP socket row")
		}
		if f[1] == endpoint && inodes[f[9]] {
			return true, nil
		}
	}
	return false, nil
}
