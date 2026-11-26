package approvedplan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// MCP-246: inside the owner's live Face ID window (admin session) a plan that
// cannot touch the control path runs without a second Face ID, like Remote
// Desktop Commander's one approval per chat. Anything that could cut the
// owner off or replace MCPshka itself keeps its own Face ID.

// protectedPrefixes may not be written (nor any ancestor of them).
var protectedPrefixes = []string{
	"/usr/local/bin", "/usr/local/libexec", "/usr/local/sbin", "/usr/bin", "/usr/sbin", "/bin", "/sbin",
	"/etc/mcpshka", "/etc/systemd", "/lib/systemd", "/usr/lib/systemd", "/etc/sudoers", "/etc/sudoers.d",
	"/etc/ssh", "/root/.ssh", "/boot", "/etc/fstab", "/etc/network", "/etc/netplan", "/etc/NetworkManager",
	"/etc/resolv.conf", "/etc/hosts", "/etc/iptables", "/etc/nftables.conf", "/etc/wireguard", "/etc/sysctl.conf", "/etc/sysctl.d",
	"/etc/lane", "/var/lib/lane", "/opt/lane", "/var/lib/mcpshka-executor", "/var/lib/mcpshka", "/etc/caddy",
	"/var/lib/tailscale", "/etc/passwd", "/etc/shadow", "/etc/group", "/etc/gshadow", "/etc/pam.d", "/etc/cron.d", "/etc/crontab",
}

// deniedExecutables change the network, power or kernel: never session-covered.
var deniedExecutables = map[string]bool{
	"reboot": true, "shutdown": true, "poweroff": true, "halt": true, "init": true, "telinit": true,
	"ip": true, "iptables": true, "ip6tables": true, "nft": true, "tc": true, "wg": true, "wg-quick": true,
	"tailscale": true, "sysctl": true, "modprobe": true, "insmod": true, "rmmod": true, "mount": true, "umount": true,
	"passwd": true, "chpasswd": true, "usermod": true, "useradd": true, "visudo": true, "dd": true, "mkfs": true,
	"sh": true, "bash": true, "dash": true, "env": true, "sudo": true, "su": true, "nsenter": true, "chroot": true,
}

var (
	systemctlVerbs = map[string]bool{"start": true, "stop": true, "restart": true, "reload": true, "enable": true, "disable": true, "status": true, "is-active": true, "daemon-reload": true}
	// protectedUnit is the control path: MCPshka, the tunnel, SSH, DNS, network.
	protectedUnit = regexp.MustCompile(`^(mcpshka|lane|xray|sing-box|tailscale|ssh|sshd|systemd-|dbus|network|NetworkManager|wpa_supplicant|caddy|dnsmasq|unbound|adguard|getty|serial-getty|docker|containerd|cron|polkit|logind)`)
	unitName      = regexp.MustCompile(`^[A-Za-z0-9@._:-]{1,96}$`)
)

// SessionScope reports why p needs its own Face ID; nil means the owner's
// live admin session may cover it.
func SessionScope(p Plan) error {
	if p.AllowDowngrade {
		return fmt.Errorf("a downgrade")
	}
	for _, w := range p.Write {
		if w == "/" || !filepath.IsAbs(w) {
			return fmt.Errorf("write %q", w)
		}
		for _, pre := range protectedPrefixes {
			if under(w, pre) || under(pre, w) {
				return fmt.Errorf("write %s touches %s", w, pre)
			}
		}
	}
	if len(p.Devices) > 0 {
		return fmt.Errorf("device access")
	}
	name := filepath.Base(p.Executable)
	if deniedExecutables[name] {
		return fmt.Errorf("executable %s", name)
	}
	if name == "systemctl" {
		return systemctlScope(p.Args)
	}
	for _, u := range p.Units {
		if protectedUnit.MatchString(u) {
			return fmt.Errorf("unit %s is on the control path", u)
		}
	}
	return nil
}

func systemctlScope(args []string) error {
	if len(args) == 0 || !systemctlVerbs[args[0]] {
		return fmt.Errorf("systemctl verb")
	}
	units := 0
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") {
			if a != "--now" && a != "--no-block" && a != "--quiet" {
				return fmt.Errorf("systemctl flag %s", a)
			}
			continue
		}
		if !unitName.MatchString(a) || protectedUnit.MatchString(a) || strings.HasSuffix(a, ".target") {
			return fmt.Errorf("systemctl unit %s", a)
		}
		units++
	}
	if units == 0 && args[0] != "daemon-reload" {
		return fmt.Errorf("systemctl without a unit")
	}
	return nil
}

func under(p, prefix string) bool {
	p, prefix = filepath.Clean(p), filepath.Clean(prefix)
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}
