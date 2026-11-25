package unitfence

import (
	"context"
	"io"
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"os"
	"strings"
	"syscall"
)

// VerifyStopped observes a registered incarnation under closed launch admission.
func (Fence) VerifyStopped(ctx context.Context, id string, i Identity) error {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) || !ValidIdentity(i) || !heavyqueue.ExecutorCgroupMatches(i.Cgroup, id) {
		return failure.ErrHold
	}
	boot, err := ReadSmall("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return failure.ErrHold
	}
	currentBoot := strings.TrimSpace(string(boot))
	if !BootIDValid(currentBoot) {
		return failure.ErrHold
	}
	var mount syscall.Statfs_t
	if syscall.Statfs("/sys/fs/cgroup", &mount) != nil || mount.Type != 0x63677270 {
		return failure.ErrHold
	}
	for range 2 {
		if currentBoot == i.BootID {
			ticks, e := ProcessTicks(i.PID)
			if (e == nil && ticks == i.StartTicks) || (e != nil && !os.IsNotExist(e)) {
				return failure.ErrHold
			}
		}
		raw, e := UnitEvidence(ctx, id)
		if e != nil || !RecoveryUnitSafe(raw, i, currentBoot) {
			return failure.ErrHold
		}
		if e = emptyOriginalCgroup(i, currentBoot != i.BootID); e != nil {
			return e
		}
		endBoot, e := ReadSmall("/proc/sys/kernel/random/boot_id")
		if e != nil || strings.TrimSpace(string(endBoot)) != currentBoot {
			return failure.ErrHold
		}
		raw, e = UnitEvidence(ctx, id)
		if e != nil || !RecoveryUnitSafe(raw, i, currentBoot) {
			return failure.ErrHold
		}
		var endMount syscall.Statfs_t
		if syscall.Statfs("/sys/fs/cgroup", &endMount) != nil || endMount.Type != mount.Type || endMount.Fsid != mount.Fsid {
			return failure.ErrHold
		}
	}
	return nil
}

func BootIDValid(id string) bool {
	return heavyqueue.ValidBootID(id)
}

func RecoveryUnitSafe(raw string, i Identity, currentBoot string) bool {
	if !BootIDValid(currentBoot) {
		return false
	}
	if currentBoot != i.BootID {
		return AbsentUnitEvidence(raw)
	}
	return VerifyUnit(raw, i, true) == nil || AbsentUnitEvidence(raw)
}

func emptyOriginalCgroup(i Identity, rebooted bool) error {
	path := "/sys/fs/cgroup" + i.Cgroup
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err != syscall.ENOENT {
			return failure.ErrHold
		}
		_, e := os.Lstat(path)
		if !os.IsNotExist(e) {
			return failure.ErrHold
		}
		return nil
	}
	defer syscall.Close(fd)
	if rebooted {
		return failure.ErrHold
	}
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || uint64(st.Dev) != i.CgroupDevice || st.Ino != i.CgroupInode {
		return failure.ErrHold
	}
	events, err := syscall.Openat(fd, "cgroup.events", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return failure.ErrHold
	}
	f := os.NewFile(uintptr(events), "reconciled-cgroup-events")
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 || !EmptyCgroupEvents(string(b)) {
		return failure.ErrHold
	}
	var pathStat syscall.Stat_t
	if syscall.Lstat(path, &pathStat) != nil || pathStat.Dev != st.Dev || pathStat.Ino != st.Ino {
		return failure.ErrHold
	}
	return nil
}

func EmptyCgroupEvents(raw string) bool {
	seen := false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "populated" {
			if seen || len(fields) != 2 || fields[1] != "0" {
				return false
			}
			seen = true
		}
	}
	return seen
}
