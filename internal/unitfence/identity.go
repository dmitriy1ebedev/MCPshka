// Package unitfence stops a transient executor unit and proves it gone: the
// systemd identity (boot, invocation, cgroup inode, pid start) recorded at
// registration, the stop-and-verify fence and the post-reboot reconcile.
package unitfence

import (
	"mcpshka/internal/failure"
	"mcpshka/internal/heavyqueue"
	"regexp"
)

var idPattern = regexp.MustCompile("^[0-9a-f]{32}$")

type Identity struct {
	BootID       string `json:"boot_id"`
	InvocationID string `json:"invocation_id"`
	Cgroup       string `json:"cgroup"`
	CgroupDevice uint64 `json:"cgroup_device"`
	CgroupInode  uint64 `json:"cgroup_inode"`
	PID          int    `json:"pid"`
	StartTicks   uint64 `json:"start_ticks"`
}

// ValidIdentity reports a fully recorded launch identity.
func ValidIdentity(i Identity) bool {
	return heavyqueue.ValidBootID(i.BootID) && idPattern.MatchString(i.InvocationID) && i.PID > 0 && i.StartTicks > 0 && i.CgroupDevice > 0 && i.CgroupInode > 0 && heavyqueue.ValidExecutorCgroup(i.Cgroup)
}

func hold(class failure.Class, code, site, message string, cause error) error {
	if failure.Details(cause) != nil {
		return cause
	}
	return failure.NewHold(class, code, site, message, cause)
}
