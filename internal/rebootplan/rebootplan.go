// Package rebootplan prepares a host reboot (MCP-111): preflight blockers
// that would leave H96 unreachable or lose work, the exact delayed reboot to
// run through approved_task (one Face ID per plan), and the post-boot checks
// whose receipt proves the host came back as planned.
package rebootplan

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Management units must be enabled, or the host boots without a way back.
var ManagementUnits = []string{"ssh.service", "tailscaled.service", "mcpshka.service", "mcpshka-broker.service"}

// Persistence files restore firewall/ipset state at boot (Lane labs, kill switch).
var PersistenceFiles = []string{"/etc/iptables/rules.v4", "/etc/ipset.rules"}

type Facts struct {
	UnitEnabled    map[string]bool `json:"unit_enabled"`
	FilesPresent   map[string]bool `json:"files_present"`
	RunningJobs    int             `json:"running_jobs"`
	ActiveLeases   int             `json:"active_leases"`
	CheckpointFile string          `json:"checkpoint_file,omitempty"`
}

type Plan struct {
	Ready     bool     `json:"ready"`
	Blockers  []string `json:"blockers,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Command   []string `json:"command"`   // run via approved_task_prepare (Face ID)
	PostBoot  []string `json:"post_boot"` // verified after boot; receipt to chat/MOTD
	Delay     string   `json:"delay"`
	CreatedAt string   `json:"created_at"`
}

// Gather reads facts without privileges; swappable in tests.
var Gather = func(ctx context.Context, jobs, leases int) Facts {
	f := Facts{UnitEnabled: map[string]bool{}, FilesPresent: map[string]bool{}, RunningJobs: jobs, ActiveLeases: leases}
	for _, u := range ManagementUnits {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		out, _ := exec.CommandContext(c, "systemctl", "is-enabled", u).Output()
		cancel()
		f.UnitEnabled[u] = strings.TrimSpace(string(out)) == "enabled"
	}
	for _, p := range PersistenceFiles {
		_, err := os.Stat(p)
		f.FilesPresent[p] = err == nil
	}
	return f
}

func Build(f Facts, now time.Time) Plan {
	p := Plan{Delay: "2m", CreatedAt: now.UTC().Format(time.RFC3339),
		Command:  []string{"systemd-run", "--on-active=2m", "--unit=mcpshka-planned-reboot", "systemctl", "reboot"},
		PostBoot: []string{"health_snapshot: failed units 0", "tailscale peer reachable", "mcpshka /healthz and tool call", "firewall/ipset rules restored (counts equal to preflight)", "project_lease_status: no lease left reclaimable without owner", "job_status of jobs interrupted by the boot reconciled"}}
	for _, u := range ManagementUnits {
		if !f.UnitEnabled[u] {
			p.Blockers = append(p.Blockers, u+" is not enabled: the host may boot without this control path")
		}
	}
	for _, file := range PersistenceFiles {
		if !f.FilesPresent[file] {
			p.Warnings = append(p.Warnings, file+" missing: rules it restores will not come back after boot")
		}
	}
	if f.RunningJobs > 0 {
		p.Blockers = append(p.Blockers, "running jobs would be killed: wait or cancel them first")
	}
	if f.ActiveLeases > 0 {
		p.Warnings = append(p.Warnings, "active project leases: holders must checkpoint; after boot they need heartbeat or resume")
	}
	if f.CheckpointFile == "" {
		p.Warnings = append(p.Warnings, "no recovery checkpoint recorded for this reboot")
	}
	p.Ready = len(p.Blockers) == 0
	return p
}
