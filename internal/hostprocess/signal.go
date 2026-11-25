package hostprocess

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"mcpshka/internal/unitbus"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func liveServiceGroups(ctx context.Context) ([]string, error) {
	raw, err := unitbus.Bounded(ctx, 65536, "show", "--property=ControlGroup", "--value", "--", "*.service")
	if err != nil {
		return nil, errors.New("cannot observe live service cgroups")
	}
	groups := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "/") && line != "/" {
			groups = append(groups, filepath.Clean(line))
		}
	}
	if len(groups) == 0 {
		return nil, errors.New("live service protection unavailable")
	}
	return groups, nil
}

// All service cgroups are protected, including descendants. Names come from
// live systemd state, so a new critical daemon needs no hand-maintained list.
func protectedProcess(pid, ppid int, groups, services []string) bool {
	if pid <= 1 || ppid <= 0 || len(groups) == 0 {
		return true
	}
	for _, cg := range groups {
		for _, part := range strings.Split(cg, "/") {
			if strings.HasSuffix(part, ".service") {
				return true
			}
		}
		for _, service := range services {
			if cg == service || strings.HasPrefix(cg, service+"/") {
				return true
			}
		}
	}
	return false
}
func RunSignal(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 3 {
		return nil, errors.New("process signal requires pid, start_ticks and signal")
	}
	pid, e := strconv.Atoi(args[0])
	if e != nil || pid <= 1 {
		return nil, errors.New("PID must exceed 1")
	}
	ticks, e := strconv.ParseUint(args[1], 10, 64)
	if e != nil || ticks == 0 {
		return nil, errors.New("observed start_ticks required")
	}
	sig, e := strconv.Atoi(args[2])
	if e != nil || sig < 1 || sig > 64 {
		return nil, errors.New("signal must be 1..64")
	}
	services, err := liveServiceGroups(ctx)
	if err != nil {
		return nil, err
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	p, err := Read("/proc", pid, 0)
	if err != nil {
		return nil, err
	}
	if p.StartTicks != ticks {
		return nil, errors.New("PID identity changed")
	}
	raw, err := ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return nil, err
	}
	if protectedProcess(pid, p.PPID, Cgroups(string(raw)), services) {
		return nil, errors.New("process belongs to a protected live service")
	}
	// Protect the broker, controller and their ancestors even outside systemd.
	for parent, seen := os.Getpid(), map[int]bool{}; parent > 1 && !seen[parent]; {
		if parent == pid {
			return nil, errors.New("management ancestor is protected")
		}
		seen[parent] = true
		ancestor, e := Read("/proc", parent, 0)
		if e != nil {
			return nil, errors.New("cannot verify management ancestry")
		}
		parent = ancestor.PPID
	}
	if err = unix.PidfdSendSignal(fd, unix.Signal(sig), nil, 0); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"pid": pid, "start_ticks": ticks, "signal": sig, "signalled": true})
}
