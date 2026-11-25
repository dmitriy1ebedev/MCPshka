package jobs

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/policy"
)

// legacyBootMargin absorbs wall-clock drift between the recorded finish time
// and the kernel boot time; a job that finished this long before the current
// boot cannot own a live process of this boot.
const legacyBootMargin = 5 * time.Minute

// bootStartedAt is the current kernel boot time (btime of /proc/stat).
// Swapped by tests.
var bootStartedAt = func() (time.Time, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || sec <= 0 {
				return time.Time{}, errors.New("invalid kernel boot time")
			}
			return time.Unix(sec, 0), nil
		}
	}
	return time.Time{}, errors.New("kernel boot time unavailable")
}

// finishedBeforeBoot proves a terminal native job without process binding is
// gone: it ended before the current kernel booted, so no process or group it
// started can exist now (MCP-101: records from before binding was added).
func finishedBeforeBoot(s Snapshot) bool {
	if s.FinishedAt.IsZero() || s.StartedAt.IsZero() || s.FinishedAt.Before(s.StartedAt) {
		return false
	}
	boot, err := bootStartedAt()
	return err == nil && s.FinishedAt.Add(legacyBootMargin).Before(boot)
}

// RecoveryResourcesGone is a non-signalling fresh resource proof for the root
// lease controller. Native terminal receipts predate resources_released; their
// exact process/group identity must be checked instead of waiting forever.
func RecoveryResourcesGone(s Snapshot) (bool, error) {
	if !isTerminal(s.State) {
		return false, nil
	}
	if s.External != nil {
		return s.ResourcesReleased, nil
	}
	if s.ResourcesReleased {
		return true, nil
	}
	if s.PID == 0 && s.CommandClass == policy.ClassReadOnly && s.StartedAt.Equal(s.FinishedAt) && !s.FinishedAt.IsZero() {
		return true, nil
	}
	if s.PID <= 0 || s.StartTicks == 0 || s.BootID == "" {
		if finishedBeforeBoot(s) {
			return true, nil
		}
		return false, errors.New("native job " + s.ID + " lacks process/group binding and finished in the current boot: HOLD")
	}
	return unknownGroupGone(&Job{PID: s.PID, StartTicks: s.StartTicks, BootID: s.BootID, ScopeUnit: s.ScopeUnit})
}
