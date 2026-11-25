package approvedplan

import "testing"

func TestSessionScopeKeepsControlPathBehindFaceID(t *testing.T) {
	ok := []Plan{
		{Executable: "/usr/bin/python3", Args: []string{"/opt/ha-noise/install.py"}, Write: []string{"/opt/ha-noise", "/etc/ha-noise"}, Units: []string{"go2rtc-ha-noise.service"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"restart", "go2rtc.service"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"daemon-reload"}},
		{Executable: "/usr/bin/docker", Args: []string{"restart", "torrserver"}},
	}
	for _, p := range ok {
		if err := SessionScope(p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	bad := []Plan{
		{Executable: "/usr/sbin/reboot"},
		{Executable: "/usr/sbin/ip", Args: []string{"route", "del", "default"}},
		{Executable: "/bin/bash", Args: []string{"-c", "x"}},
		{Executable: "/usr/bin/cp", Write: []string{"/usr/local/bin"}},
		{Executable: "/usr/bin/cp", Write: []string{"/etc"}},
		{Executable: "/usr/bin/cp", Write: []string{"/etc/ssh/sshd_config.d"}},
		{Executable: "/usr/bin/cp", Write: []string{"/var/lib/lane/x"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"stop", "mcpshka.service"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"restart", "lane-xray.service"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"isolate", "rescue.target"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"start", "rescue.target"}},
		{Executable: "/usr/bin/systemctl", Args: []string{"--force", "restart", "go2rtc"}},
		{Executable: "/usr/bin/python3", Units: []string{"tailscaled.service"}},
		{Executable: "/usr/bin/tee", Devices: []string{"/dev/sda"}},
		{Executable: "/x", AllowDowngrade: true},
	}
	for _, p := range bad {
		if SessionScope(p) == nil {
			t.Fatalf("session admitted %+v", p)
		}
	}
}
