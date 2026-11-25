// Package installplan is the idempotent first-run bootstrap preview
// (MCP-079): preflight of architecture, ports, systemd, disk and an existing
// install, then an explicit step plan with blast radius and rollback. It
// changes nothing; the steps run only through the release/install tools.
package installplan

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

type Facts struct {
	Arch        string       `json:"arch"`
	Systemd     bool         `json:"systemd"`
	FreeBytes   uint64       `json:"free_bytes"`
	PortsBusy   map[int]bool `json:"ports_busy"`
	Installed   string       `json:"installed_version,omitempty"`
	ServiceUser bool         `json:"service_user_exists"`
}

type Step struct {
	Name     string `json:"name"`
	Blast    string `json:"blast_radius"`
	Rollback string `json:"rollback"`
	Skip     bool   `json:"skip,omitempty"` // already in place (idempotent)
}

type Plan struct {
	Ready    bool     `json:"ready"`
	Blockers []string `json:"blockers,omitempty"`
	Steps    []Step   `json:"steps"`
	PanelURL string   `json:"panel_hint"`
}

var Ports = []int{8765, 8799} // MCP HTTP and the approval portal (loopback), as observed on H96
const MinFree = 2 << 30       // binaries, state, one rollback release

func Gather(installed string) Facts {
	f := Facts{Arch: runtime.GOARCH, PortsBusy: map[int]bool{}, Installed: installed}
	_, err := os.Stat("/run/systemd/system")
	f.Systemd = err == nil
	var st syscall.Statfs_t
	if syscall.Statfs("/var/lib", &st) == nil {
		f.FreeBytes = st.Bavail * uint64(st.Bsize)
	}
	for _, p := range Ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			f.PortsBusy[p] = true
			continue
		}
		l.Close()
	}
	_, err = os.Stat("/var/lib/mcpshka")
	f.ServiceUser = err == nil
	return f
}

func Build(f Facts) Plan {
	p := Plan{PanelURL: "after install: owner_portal (one screen) and the Face ID enrollment link from admin_session_prepare"}
	if f.Arch != "arm64" && f.Arch != "amd64" {
		p.Blockers = append(p.Blockers, "unsupported architecture "+f.Arch)
	}
	if !f.Systemd {
		p.Blockers = append(p.Blockers, "systemd not running: units cannot be supervised")
	}
	if f.FreeBytes < MinFree {
		p.Blockers = append(p.Blockers, fmt.Sprintf("free space %d MiB < %d MiB on /var/lib", f.FreeBytes>>20, MinFree>>20))
	}
	for _, port := range Ports {
		if f.PortsBusy[port] && f.Installed == "" {
			p.Blockers = append(p.Blockers, fmt.Sprintf("port %d busy before install: another service would be shadowed", port))
		}
	}
	p.Steps = []Step{
		{Name: "create system user mcpshka (no login, no sudo)", Blast: "new account only", Rollback: "userdel mcpshka", Skip: f.ServiceUser},
		{Name: "install pinned, checksummed release to /opt/mcpshka/releases/<version>", Blast: "new directory", Rollback: "remove the release directory"},
		{Name: "write units mcpshka.service and mcpshka-broker.service (loopback only)", Blast: "two new units", Rollback: "systemctl disable --now; remove units"},
		{Name: "initialize state at /var/lib/mcpshka (0700, owned by mcpshka)", Blast: "new state directory", Rollback: "keep (state is never deleted automatically)", Skip: f.Installed != ""},
		{Name: "switch current symlink atomically, start, health+readiness", Blast: "MCPshka only", Rollback: "release_apply to the previous version (kept)"},
	}
	p.Ready = len(p.Blockers) == 0
	return p
}
