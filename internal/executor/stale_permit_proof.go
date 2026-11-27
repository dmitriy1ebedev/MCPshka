package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/unitfence"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux exposes /proc starttime in USER_HZ (100), independent of kernel HZ.
// A recorded launch proves its permit existed by that time. Across boots,
// current boot time is a conservative upper bound on the legacy permit's age.
func legacyPermitAge(s Snapshot, boot string, uptime time.Duration) bool {
	if uptime <= 0 {
		return false
	}
	for _, l := range s.Launches {
		if l.Identity == nil || !unitfence.ValidIdentity(*l.Identity) {
			continue
		}
		age := uptime
		if l.Identity.BootID == boot {
			if l.Identity.StartTicks > uint64(uptime/(10*time.Millisecond)) {
				continue
			}
			age -= time.Duration(l.Identity.StartTicks) * 10 * time.Millisecond
		}
		// sysinfo uptime rounds up to seconds; starttime has 10 ms precision.
		if age > time.Hour+2*time.Second {
			return true
		}
	}
	return false
}

func proveStalePermit(ctx context.Context, s Snapshot, now time.Time) error {
	if os.Geteuid() != 0 || (s.State != "ACTIVE" && s.State != "REVOKING") {
		return ErrHold
	}
	boot, err := unitfence.ReadSmall("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ErrHold
	}
	bootID := strings.TrimSpace(string(boot))
	if !unitfence.BootIDValid(bootID) {
		return ErrHold
	}
	var info syscall.Sysinfo_t
	if syscall.Sysinfo(&info) != nil {
		return ErrHold
	}
	old := !s.CreatedAt.IsZero() && s.CreatedAt.Before(now.Add(-time.Hour))
	ids := map[string]bool{}
	for _, l := range s.Launches {
		ids[l.ID] = true
	}
	// A sealed root-owned manifest also provides a conservative age bound for
	// legacy permits without a registered identity (including empty permits).
	path := filepath.Join(AuthorityRoot, s.Pin.PermitID+".manifest.json")
	if m, err := readManifest(s.Pin.PermitID); err == nil {
		if m.Pin != s.Pin {
			return ErrHold
		}
		for id := range m.Runs {
			if !idPattern.MatchString(id) {
				return ErrHold
			}
			ids[id] = true
		}
		st, err := os.Lstat(path)
		if err != nil {
			return ErrHold
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.Mode().IsRegular() || stat.Uid != 0 || stat.Nlink != 1 || st.Mode().Perm()&0077 != 0 {
			return ErrHold
		}
		if s.CreatedAt.IsZero() && st.ModTime().Before(now.Add(-time.Hour)) && time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec).Before(now.Add(-time.Hour)) {
			old = true
		}
	} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return ErrHold
	}
	if s.CreatedAt.IsZero() && legacyPermitAge(s, bootID, time.Duration(info.Uptime)*time.Second) {
		old = true
	}
	if !old {
		return ErrHold
	}
	for _, l := range s.Launches {
		if l.Identity != nil {
			if (SystemdFence{}).VerifyStopped(ctx, l.ID, *l.Identity) != nil {
				return ErrHold
			}
		}
	}
	for id := range ids {
		job, err := unitfence.Systemctl(ctx, "show", "--property=Job", "--value", "--", "mcpshka-executor-"+id+".service")
		if err != nil || strings.TrimSpace(job) != "" {
			return ErrHold
		}
		raw, err := unitfence.UnitEvidence(ctx, id)
		if err != nil || !staleUnitInactive(raw, id) {
			return ErrHold
		}
		for _, group := range []string{heavyqueue.ExecutorCgroup(id), "/system.slice/mcpshka-executor-" + id + ".service"} {
			cg := "/sys/fs/cgroup" + group
			if _, err := os.Lstat(cg); err == nil {
				// Registered identities are checked against the original inode above.
				registered := false
				for _, l := range s.Launches {
					if l.ID == id && l.Identity != nil && l.Identity.Cgroup == group {
						registered = true
					}
				}
				if !registered {
					return ErrHold
				}
			} else if !os.IsNotExist(err) {
				return ErrHold
			}
		}
	}
	if err := noStalePermitProcesses(ctx, "/proc", s.Pin.PermitID, ids); err != nil {
		return err
	}
	end, err := unitfence.ReadSmall("/proc/sys/kernel/random/boot_id")
	if err != nil || !bytes.Equal(boot, end) {
		return ErrHold
	}
	return nil
}
func staleUnitInactive(raw, id string) bool {
	props := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return false
		}
		if _, dup := props[k]; dup {
			return false
		}
		props[k] = v
	}
	if _, ok := props["InvocationID"]; !ok {
		return false
	}
	if _, ok := props["ControlGroup"]; !ok {
		return false
	}
	if props["ControlGroup"] != "" && !heavyqueue.ExecutorCgroupMatches(props["ControlGroup"], id) {
		return false
	}
	if props["InvocationID"] != "" && !idPattern.MatchString(props["InvocationID"]) {
		return false
	}
	if props["LoadState"] == "not-found" && (props["InvocationID"] != "" || props["ControlGroup"] != "") {
		return false
	}
	return len(props) == 7 && (props["LoadState"] == "not-found" || props["LoadState"] == "loaded") && (props["ActiveState"] == "inactive" || props["ActiveState"] == "failed") && props["MainPID"] == "0" && props["Restart"] == "no" && props["KillMode"] == "control-group"
}
func noStalePermitProcesses(ctx context.Context, procRoot, permit string, ids map[string]bool) error {
	dir, err := os.Open(procRoot)
	if err != nil {
		return ErrHold
	}
	defer dir.Close()
	entries, err := dir.ReadDir(4097)
	if err != nil || len(entries) > 4096 {
		return ErrHold
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		pidPath := filepath.Join(procRoot, entry.Name())
		// Plans travel as one argv value to the fixed root worker. A maximal
		// plan exceeds the normal 8 KiB evidence bound, but is still bounded.
		f, err := os.Open(pidPath + "/cmdline")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ErrHold
		}
		cmd, err := io.ReadAll(io.LimitReader(f, 65537))
		f.Close()
		if err == nil && len(cmd) > 65536 {
			return ErrHold
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ErrHold
		}
		for _, arg := range bytes.Split(cmd, []byte{0}) {
			if string(arg) == permit || ids[string(arg)] {
				return ErrHold
			}
		}
		cg, err := unitfence.ReadSmall(pidPath + "/cgroup")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ErrHold
		}
		for id := range ids {
			if strings.Contains(string(cg), fmt.Sprintf("/mcpshka-executor-%s.service", id)) {
				return ErrHold
			}
		}
	}
	return nil
}
