package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ProcessObservation struct {
	ObservedAt   time.Time `json:"observed_at"`
	Alive        bool      `json:"alive"`
	ProcessCount int       `json:"process_count"`
	LeaderState  string    `json:"leader_state,omitempty"`
	CPUTicks     uint64    `json:"cpu_ticks"`
	CPUSeconds   uint64    `json:"cpu_seconds,omitempty"`
	RSSBytes     uint64    `json:"rss_bytes"`
	ReadBytes    uint64    `json:"read_bytes"`
	WriteBytes   uint64    `json:"write_bytes"`
}

type procStat struct {
	pid        int
	pgrp       int
	state      string
	utime      uint64
	stime      uint64
	rssPages   int64
	startTicks uint64
}

func observeProcessGroup(pgid int) ProcessObservation {
	obs := ProcessObservation{ObservedAt: time.Now().UTC()}
	if pgid <= 0 {
		return obs
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return obs
	}
	pageSize := int64(os.Getpagesize())
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		st, err := parseProcStat(string(raw))
		if err != nil || st.pgrp != pgid {
			continue
		}
		obs.Alive = true
		obs.ProcessCount++
		if pid == pgid {
			obs.LeaderState = st.state
		}
		obs.CPUTicks += st.utime + st.stime
		if st.rssPages > 0 && pageSize > 0 {
			obs.RSSBytes += uint64(st.rssPages * pageSize)
		}
		readBytes, writeBytes := readProcIO(pid)
		obs.ReadBytes += readBytes
		obs.WriteBytes += writeBytes
	}
	return obs
}

func parseProcStat(raw string) (procStat, error) {
	open := strings.IndexByte(raw, '(')
	close := strings.LastIndexByte(raw, ')')
	if open <= 0 || close <= open {
		return procStat{}, errors.New("invalid proc stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(raw[:open]))
	if err != nil {
		return procStat{}, err
	}
	fields := strings.Fields(strings.TrimSpace(raw[close+1:]))
	if len(fields) < 22 {
		return procStat{}, errors.New("short proc stat")
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return procStat{}, err
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return procStat{}, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return procStat{}, err
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return procStat{}, err
	}
	rssPages, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil {
		return procStat{}, err
	}
	return procStat{
		pid:        pid,
		pgrp:       pgrp,
		state:      fields[0],
		utime:      utime,
		stime:      stime,
		rssPages:   rssPages,
		startTicks: startTicks,
	}, nil
}

func readProcIO(pid int) (uint64, uint64) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "io"))
	if err != nil {
		return 0, 0
	}
	var readBytes, writeBytes uint64
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(name) {
		case "read_bytes":
			readBytes = n
		case "write_bytes":
			writeBytes = n
		}
	}
	return readBytes, writeBytes
}

func bootIdentity() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if len(id) != 36 {
		return "", errors.New("invalid kernel boot identity")
	}
	return id, nil
}
func processIdentity(pid int) (string, uint64, error) {
	boot, err := bootIdentity()
	if err != nil {
		return "", 0, err
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, err
	}
	stat, err := parseProcStat(string(raw))
	if err != nil || stat.pid != pid || stat.startTicks == 0 {
		return "", 0, errors.New("process identity unavailable")
	}
	return boot, stat.startTicks, nil
}

// Only prove resource disappearance. Never infer exit status or signal an
// unowned process; a reused PID must not turn reconciliation into cancellation.
func unknownGroupGone(j *Job) (bool, error) {
	if j.External != nil {
		return false, errors.New("external resource ownership requires runtime reconciliation: HOLD")
	}
	if j.PID <= 0 || j.BootID == "" || j.StartTicks == 0 {
		return false, errors.New("legacy job lacks PID/start-time/boot binding: HOLD")
	}
	boot, err := bootIdentity()
	if err != nil {
		return false, err
	}
	if boot != j.BootID {
		return true, nil
	}
	if j.ScopeUnit != "" {
		gone, err := scopeGone(j)
		if err != nil || !gone {
			return false, err
		}
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(j.PID), "stat"))
	if err == nil {
		st, parseErr := parseProcStat(string(raw))
		if parseErr != nil {
			return false, parseErr
		}
		if st.startTicks == j.StartTicks {
			return false, errors.New("bound process still exists: HOLD")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	err = syscall.Kill(-j.PID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, err
	}
	return false, errors.New("process group still exists or cannot be inspected: HOLD")
}
