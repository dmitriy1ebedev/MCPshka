package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MCP-067: before launching a job, refuse when the service's own cgroup is
// close to its pids limit. A job started there would fail with EAGAIN or
// time out and look like an application bug; raising TasksMax blindly is
// not the fix. Jobs themselves run in their own scope with TasksMax and are
// reaped as a whole cgroup.

// ErrTasksExhausted is the typed admission refusal.
var ErrTasksExhausted = errors.New("TASKS_EXHAUSTED")

// tasksHeadroomReserve is the fraction of pids.max kept free for the service.
const tasksHeadroomReserve = 10 // percent

var (
	cgroupRoot     = "/sys/fs/cgroup"
	selfCgroupFile = "/proc/self/cgroup"
)

// TasksHeadroom reports the pids usage of this process's cgroup; ok=false
// when it cannot be read or has no limit (then admission is not blocked).
func TasksHeadroom() (current, limit int, ok bool) {
	raw, err := os.ReadFile(selfCgroupFile)
	if err != nil {
		return 0, 0, false
	}
	path := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if rest, found := strings.CutPrefix(line, "0::"); found {
			path = rest
		}
	}
	if path == "" || strings.Contains(path, "..") {
		return 0, 0, false
	}
	dir := filepath.Join(cgroupRoot, path)
	if filepath.Base(dir) == "server" {
		// Delegated split (cgroup.go): the service limit sits one level up.
		dir = filepath.Dir(dir)
	}
	read := func(name string) (int, bool) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return 0, false
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return v, err == nil
	}
	cur, okc := read("pids.current")
	max, okm := read("pids.max") // "max" (unlimited) fails Atoi: no limit
	if !okc || !okm || max <= 0 {
		return cur, 0, false
	}
	return cur, max, true
}

func checkTasksHeadroom() error {
	cur, max, ok := TasksHeadroom()
	if !ok || cur*100 < max*(100-tasksHeadroomReserve) {
		return nil
	}
	return errors.Join(ErrTasksExhausted, errors.New("service cgroup uses "+strconv.Itoa(cur)+" of "+strconv.Itoa(max)+
		" tasks; wait for running jobs to finish (job_status) and check host_triage for leaked descendants before raising TasksMax"))
}
