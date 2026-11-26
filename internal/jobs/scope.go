package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/unitbus"
)

// scopeGone requires both a fresh systemd state and kernel cgroup evidence.
// A failed query or unreadable membership is never evidence of disappearance.
func scopeGone(j *Job) (bool, error) {
	if !validScopeUnit(j.ScopeUnit) || j.ScopeUnit != "mcpshka-job-"+j.ID+".scope" {
		return false, errors.New("scope identity mismatch: HOLD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := []string{"show", "--property=LoadState", "--property=ActiveState", "--property=ControlGroup", "--", j.ScopeUnit}
	if raw, ok := unitbus.Show(ctx, args...); ok && len(raw) <= 4096 {
		return scopeStateGone(j.ScopeUnit, string(raw))
	}
	c := exec.CommandContext(ctx, systemctlPath, args...)
	var out bytes.Buffer
	q := &outputQuota{limit: 4096}
	c.Stdout = &quotaWriter{dst: &out, quota: q}
	c.Stderr = &quotaWriter{dst: io.Discard, quota: q}
	if err := c.Run(); err != nil {
		return false, errors.New("scope status unavailable: HOLD")
	}
	_, dropped := q.stats()
	if dropped != 0 {
		return false, errors.New("scope status exceeded budget: HOLD")
	}
	return scopeStateGone(j.ScopeUnit, out.String())
}

func scopeStateGone(unit, raw string) (bool, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false, errors.New("invalid scope status")
		}
		if _, duplicate := fields[key]; duplicate {
			return false, errors.New("duplicate scope property")
		}
		fields[key] = value
	}
	if len(fields) != 3 {
		return false, errors.New("incomplete scope status")
	}
	load, active, group := fields["LoadState"], fields["ActiveState"], fields["ControlGroup"]
	if active != "inactive" && active != "failed" {
		return false, errors.New("scope still active: HOLD")
	}
	if load != "loaded" && load != "not-found" {
		return false, errors.New("scope load state unknown: HOLD")
	}
	expected := heavyqueue.HeavyCgroup + "/" + unit
	if group != "" && group != expected && group != "/system.slice/"+unit {
		return false, errors.New("scope cgroup binding mismatch")
	}
	if load == "not-found" && group != "" {
		return false, errors.New("missing scope has cgroup binding")
	}
	return scopeCgroupsGone(unit, os.ReadFile)
}

func scopeCgroupsGone(unit string, read func(string) ([]byte, error)) (bool, error) {
	for _, group := range []string{heavyqueue.HeavyCgroup, "/system.slice"} {
		raw, err := read("/sys/fs/cgroup" + group + "/" + unit + "/cgroup.events")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if len(raw) > 4096 {
			return false, errors.New("cgroup events exceeded budget")
		}
		empty := false
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "populated 0" {
				if empty {
					return false, errors.New("duplicate population evidence")
				}
				empty = true
			}
			if line == "populated 1" {
				return false, errors.New("scope descendants remain: HOLD")
			}
		}
		if !empty {
			return false, errors.New("cgroup population unknown: HOLD")
		}
	}
	return true, nil
}

const (
	systemdRunPath = "/usr/bin/systemd-run"
	systemctlPath  = "/usr/bin/systemctl"
)

func (m *Manager) EnableSystemdScopes() error {
	for _, path := range []string{systemdRunPath, systemctlPath} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("systemd scope helper %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("systemd scope helper %s is not executable", path)
		}
	}
	if info, err := os.Stat("/run/systemd/system"); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return fmt.Errorf("systemd runtime unavailable: %w", err)
	}
	m.mu.Lock()
	m.scopeEnabled = true
	m.mu.Unlock()
	return nil
}

func (m *Manager) SystemdScopesEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scopeEnabled
}

func buildScopedCommand(id, command string, args []string, memoryLimit int64, cpuQuotaPercent, processLimit int) (string, *exec.Cmd) {
	unit := "mcpshka-job-" + id + ".scope"
	tasksMax := processLimit * 4
	if tasksMax < 64 {
		tasksMax = 64
	}
	if tasksMax > 1024 {
		tasksMax = 1024
	}
	scopeArgs := []string{
		"--scope",
		"--quiet",
		"--collect",
		"--unit=" + unit,
		"--property=MemoryMax=" + strconv.FormatInt(memoryLimit, 10),
		"--property=CPUQuota=" + strconv.Itoa(cpuQuotaPercent) + "%",
		"--property=TasksMax=" + strconv.Itoa(tasksMax),
		"--",
		command,
	}
	scopeArgs = append(scopeArgs, args...)
	return unit, exec.Command(systemdRunPath, heavyqueue.HeavyUnitArgs(scopeArgs)...)
}

func observeScope(unit string) ProcessObservation {
	if !validScopeUnit(unit) {
		return ProcessObservation{ObservedAt: time.Now().UTC()}
	}
	return observeUnitCgroup(unit)
}

var executorUnit = regexp.MustCompile(`^mcpshka-executor-[a-f0-9]{32}\.service$`)

// observeExecutorUnit reads a sealed test's executor cgroup (read-only):
// CPU, memory, I/O and process count show a long silent matrix is alive
// before it prints anything (DIAG ACTIVE-03, MCP-124).
func observeExecutorUnit(unit string) ProcessObservation {
	if !executorUnit.MatchString(unit) {
		return ProcessObservation{ObservedAt: time.Now().UTC()}
	}
	return observeUnitCgroup(unit)
}

func observeUnitCgroup(unit string) ProcessObservation {
	obs := observeCgroup(filepath.Join(cgroupRoot, strings.TrimPrefix(heavyqueue.HeavyCgroup, "/"), unit))
	if !obs.Alive {
		return observeCgroup(filepath.Join(cgroupRoot, "system.slice", unit))
	}
	return obs
}

func observeCgroup(root string) ProcessObservation {
	obs := ProcessObservation{ObservedAt: time.Now().UTC()}
	raw, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		return obs
	}
	var pids []int
	for _, line := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	if len(pids) == 0 {
		return obs
	}
	obs.Alive = true
	obs.ProcessCount = len(pids)

	if raw, err := os.ReadFile(filepath.Join(root, "memory.current")); err == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			obs.RSSBytes = n
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "cpu.stat")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "usage_usec" {
				if usec, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					obs.CPUTicks = usec * defaultClockTicks / 1_000_000
					obs.CPUSeconds = usec / 1_000_000
				}
				break
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "io.stat")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			for _, field := range strings.Fields(line) {
				name, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				n, err := strconv.ParseUint(value, 10, 64)
				if err != nil {
					continue
				}
				switch name {
				case "rbytes":
					obs.ReadBytes += n
				case "wbytes":
					obs.WriteBytes += n
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pids[0]), "stat")); err == nil {
		if st, err := parseProcStat(string(raw)); err == nil {
			obs.LeaderState = st.state
		}
	}
	return obs
}

func validScopeUnit(unit string) bool {
	if !strings.HasPrefix(unit, "mcpshka-job-") || !strings.HasSuffix(unit, ".scope") || len(unit) > 128 {
		return false
	}
	for _, r := range unit {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func terminateScope(unit string, signal syscall.Signal) {
	if !validScopeUnit(unit) {
		return
	}
	name := "TERM"
	if signal == syscall.SIGKILL {
		name = "KILL"
	}
	cmd := exec.Command(systemctlPath, "kill", "--kill-who=all", "--signal="+name, unit)
	_ = cmd.Run()
	if signal == syscall.SIGKILL {
		_ = exec.Command(systemctlPath, "stop", unit).Run()
	}
}
