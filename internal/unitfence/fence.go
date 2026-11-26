package unitfence

import (
	"context"
	"fmt"
	"io"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/unitbus"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// systemctl reads plain show queries over the bus with the same 8 KiB budget.
func Systemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := unitbus.Bounded(ctx, 8192, args...)
	if err != nil {
		return "", failure.ErrHold
	}
	return string(out), nil
}
func UnitEvidence(ctx context.Context, id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", failure.ErrHold
	}
	return Systemctl(ctx, "show", "--no-pager", "--property=InvocationID", "--property=ControlGroup", "--property=MainPID", "--property=Restart", "--property=KillMode", "--property=ActiveState", "--property=LoadState", "--", "mcpshka-executor-"+id+".service")
}
func VerifyUnit(raw string, i Identity, stopped bool) error {
	if len(raw) > 8192 {
		return failure.ErrHold
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return failure.ErrHold
		}
		if _, dup := m[k]; dup {
			return failure.ErrHold
		}
		m[k] = v
	}
	if len(m) != 7 || m["InvocationID"] != i.InvocationID || m["Restart"] != "no" || m["KillMode"] != "control-group" || m["LoadState"] != "loaded" {
		return failure.ErrHold
	}
	if stopped {
		if (m["ActiveState"] != "inactive" && m["ActiveState"] != "failed") || m["MainPID"] != "0" || (m["ControlGroup"] != "" && m["ControlGroup"] != i.Cgroup) {
			return failure.ErrHold
		}
	} else {
		if m["ActiveState"] != "active" || m["MainPID"] != strconv.Itoa(i.PID) || m["ControlGroup"] != i.Cgroup {
			return failure.ErrHold
		}
	}
	return nil
}
func ReadSmall(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 8193))
	if e != nil || len(b) > 8192 {
		return nil, failure.ErrHold
	}
	return b, nil
}
func ProcessTicks(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, failure.ErrHold
	}
	b, e := ReadSmall(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return 0, e
	}
	at := strings.LastIndexByte(string(b), ')')
	if at < 0 {
		return 0, failure.ErrHold
	}
	f := strings.Fields(string(b[at+1:]))
	if len(f) < 20 {
		return 0, failure.ErrHold
	}
	n, e := strconv.ParseUint(f[19], 10, 64)
	if e != nil || n == 0 {
		return 0, failure.ErrHold
	}
	return n, nil
}

// Fence takes registered identities only. Missing evidence fails closed;
// already stopped units after lost replies require durable reconciliation.
type Fence struct{}

func (Fence) StopAndVerify(ctx context.Context, id string, i Identity) error {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) || !ValidIdentity(i) || !heavyqueue.ExecutorCgroupMatches(i.Cgroup, id) {
		return failure.ErrHold
	}
	boot, e := ReadSmall("/proc/sys/kernel/random/boot_id")
	if e != nil || strings.TrimSpace(string(boot)) != i.BootID {
		return failure.ErrHold
	}
	raw, e := UnitEvidence(ctx, id)
	if e != nil {
		return e
	}
	if e = VerifyUnit(raw, i, false); e != nil {
		return e
	}
	ticks, e := ProcessTicks(i.PID)
	if e != nil || ticks != i.StartTicks {
		return failure.ErrHold
	}
	fd, e := syscall.Open("/sys/fs/cgroup"+i.Cgroup, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return failure.ErrHold
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || uint64(st.Dev) != i.CgroupDevice || st.Ino != i.CgroupInode {
		return failure.ErrHold
	}
	// Pin the evidence file while the original cgroup still exists.
	events, e := syscall.Openat(fd, "cgroup.events", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return failure.ErrHold
	}
	f := os.NewFile(uintptr(events), "pinned-cgroup-events")
	defer f.Close()
	if _, e = Systemctl(ctx, "stop", "--", "mcpshka-executor-"+id+".service"); e != nil {
		return e
	}
	raw, e = UnitEvidence(ctx, id)
	if e != nil {
		return e
	}
	if e = verifyStoppedFenceUnit(raw, i, fd, st); e != nil {
		return e
	}
	ticks, e = ProcessTicks(i.PID)
	if e == nil && ticks == i.StartTicks {
		return failure.ErrHold
	}
	if e != nil && !os.IsNotExist(e) {
		return failure.ErrHold
	}
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(b) > 4096 {
		var deleted syscall.Stat_t
		_, pathErr := os.Lstat("/sys/fs/cgroup" + i.Cgroup)
		if syscall.Fstat(fd, &deleted) != nil || !VerifiedDeletedCgroup(st, deleted, os.IsNotExist(pathErr), pinnedFdDeleted(fd, i.Cgroup)) {
			return failure.ErrHold
		}
		// The same stopped unit was verified above. A replacement path, unreadable
		// path or still-linked original directory is never disappearance evidence.
		raw, e = UnitEvidence(ctx, id)
		if e != nil {
			return e
		}
		return verifyStoppedFenceUnit(raw, i, fd, st)
	}
	populated := false
	seen := false
	for _, l := range strings.Split(string(b), "\n") {
		if l == "populated 0" {
			if seen {
				return failure.ErrHold
			}
			seen = true
		}
		if l == "populated 1" {
			populated = true
			seen = true
		}
	}
	if !seen || populated {
		return failure.ErrHold
	}
	// Re-read systemd after kernel evidence to catch a reincarnation/repopulation.
	raw, e = UnitEvidence(ctx, id)
	if e != nil {
		return e
	}
	return verifyStoppedFenceUnit(raw, i, fd, st)
}

func VerifiedDeletedCgroup(original, current syscall.Stat_t, pathMissing, fdDeleted bool) bool {
	return pathMissing && fdDeleted && original.Dev == current.Dev && original.Ino == current.Ino
}
func pinnedFdDeleted(fd int, cgroup string) bool {
	link, e := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	return e == nil && link == "/sys/fs/cgroup"+cgroup+" (deleted)"
}

// Unit GC is accepted only after an acknowledged stop of the previously pinned
// invocation, with the original cgroup inode deleted and no replacement path.
func verifyStoppedFenceUnit(raw string, i Identity, fd int, original syscall.Stat_t) error {
	if VerifyUnit(raw, i, true) == nil {
		return nil
	}
	if !AbsentUnitEvidence(raw) {
		return failure.ErrHold
	}
	var current syscall.Stat_t
	_, pathErr := os.Lstat("/sys/fs/cgroup" + i.Cgroup)
	if syscall.Fstat(fd, &current) != nil || !VerifiedDeletedCgroup(original, current, os.IsNotExist(pathErr), pinnedFdDeleted(fd, i.Cgroup)) {
		return failure.ErrHold
	}
	return nil
}
func AbsentUnitEvidence(raw string) bool {
	if len(raw) > 8192 {
		return false
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return false
		}
		if _, dup := m[k]; dup {
			return false
		}
		m[k] = v
	}
	if _, ok := m["InvocationID"]; !ok {
		return false
	}
	if _, ok := m["ControlGroup"]; !ok {
		return false
	}
	return len(m) == 7 && m["LoadState"] == "not-found" && m["ActiveState"] == "inactive" && m["MainPID"] == "0" && m["InvocationID"] == "" && m["ControlGroup"] == "" && m["Restart"] == "no" && m["KillMode"] == "control-group"
}

// ReleaseUnit clears a fenced unit's failed state. Fixed argv, bounded time;
// an already collected or inactive unit makes it a no-op.
func (Fence) ReleaseUnit(ctx context.Context, id string) {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "reset-failed", "mcpshka-executor-"+id+".service")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	_ = cmd.Run()
}
