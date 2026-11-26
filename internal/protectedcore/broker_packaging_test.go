package protectedcore

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func brokerRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestBrokerPackagingAllowsOnlyBoundedTrafficCapture(t *testing.T) {
	root := brokerRepoRoot(t)
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "systemd", "mcpshka-broker.service"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"ProtectSystem=strict",
		"ReadWritePaths=/var/lib/diag/traffic-runs",
		"RestrictAddressFamilies=AF_UNIX AF_NETLINK AF_PACKET",
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW",
		"AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW",
		"NoNewPrivileges=true",
		// Only the protected integration store is exposed from /root, read-only.
		"ProtectHome=tmpfs",
		"BindReadOnlyPaths=-/root/repos/mcpshka-controlled\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("broker unit missing %q", want)
		}
	}
	for _, forbidden := range []string{"AF_INET ", "AF_INET6", "CAP_SYS_ADMIN", "ReadWritePaths=/ ", "ProtectHome=true", "ProtectHome=read-only", "BindPaths=", "ReadWritePaths=/root"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("broker unit unexpectedly contains %q", forbidden)
		}
	}

	installer, err := os.ReadFile(filepath.Join(root, "packaging", "native", "install-broker.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installer)
	for _, want := range []string{
		"TRAFFIC_RUNS_ROOT=\"/var/lib/diag/traffic-runs\"",
		"install -d -o root -g mcpshka -m 2770 \"$TRAFFIC_RUNS_ROOT\"",
	} {
		if !strings.Contains(installText, want) {
			t.Fatalf("broker installer missing %q", want)
		}
	}

}

func TestBrokerResourceUpgradeAndRollbackRegression(t *testing.T) {
	root := brokerRepoRoot(t)
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "systemd", "mcpshka-broker.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"CPUQuota=100%\n", "CPUWeight=400\n", "IOWeight=400\n", "TasksMax=96\n"} {
		if !strings.Contains(string(unit), setting) {
			t.Fatalf("packaged broker resource contract missing %q", setting)
		}
	}
	script := filepath.Join(root, "packaging", "native", "test_coordinated_upgrade.py")
	cmd := exec.Command("python3", "-I", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "PYTHONDONTWRITEBYTECODE=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("paired release resource, rollback and fail-closed regressions: %v: %s", err, out)
	}
}
