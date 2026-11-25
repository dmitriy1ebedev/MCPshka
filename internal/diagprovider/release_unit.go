package diagprovider

import (
	"mcpshka/internal/heavyqueue"
	"strings"
)

// ReleaseUnit names the release worker unit of one DIAG version.
func ReleaseUnit(version string) string {
	return "mcpshka-diag-release-" + strings.NewReplacer(".", "", "-", "").Replace(version)
}

// ReleaseUnitArgs are the systemd-run arguments of a DIAG release worker. The
// build yields the CPU to the gateway's network services (CPUWeight 20 like
// every other heavy unit; 14.11.2026 a DIAG build beside agent tests drove load
// to 14 and the home internet stalled) and waits its turn in the host heavy
// queue inside the worker.
func ReleaseUnitArgs(unit, commit, version string) []string {
	return heavyqueue.HeavyUnitArgs([]string{"--unit=" + unit, "--collect", "--no-block", "--quiet",
		"--property=Type=oneshot", "--property=User=root", "--property=Group=root", "--property=UMask=0022",
		"--property=RuntimeMaxSec=1800s", "--property=MemoryMax=1G", "--property=CPUQuota=200%", "--property=TasksMax=64",
		"--property=RestrictAddressFamilies=AF_UNIX",
		"/usr/local/libexec/mcpshka-broker", "diag-release-worker", commit, version})
}
