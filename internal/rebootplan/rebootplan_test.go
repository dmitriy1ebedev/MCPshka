package rebootplan

import (
	"strings"
	"testing"
	"time"
)

// MCP-111: a disabled control path or running work blocks the reboot; a
// ready plan is a delayed reboot with post-boot verification.
func TestRebootPlanBlocksUnsafeHostAndVerifiesAfterBoot(t *testing.T) {
	f := Facts{UnitEnabled: map[string]bool{}, FilesPresent: map[string]bool{"/etc/iptables/rules.v4": true}, RunningJobs: 1}
	for _, u := range ManagementUnits {
		f.UnitEnabled[u] = true
	}
	f.UnitEnabled["tailscaled.service"] = false
	p := Build(f, time.Now())
	if p.Ready || len(p.Blockers) != 2 || !strings.Contains(p.Blockers[0], "tailscaled") {
		t.Fatalf("unsafe plan: %+v", p)
	}
	f.UnitEnabled["tailscaled.service"], f.RunningJobs, f.CheckpointFile = true, 0, "/root/ckpt.md"
	p = Build(f, time.Now())
	if !p.Ready || p.Command[1] != "--on-active=2m" || len(p.PostBoot) < 4 || len(p.Warnings) != 1 {
		t.Fatalf("ready plan: %+v", p)
	}
}
