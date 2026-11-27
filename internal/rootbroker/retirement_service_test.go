package rootbroker

import (
	"reflect"
	"strings"
	"testing"
)

func retirementTestArgs() []string {
	return []string{"retire-deployment", strings.Repeat("a", 32), "owner", "1792159092900-1e327387d562", strings.Repeat("b", 64), strings.Repeat("c", 64), "inspected UNKNOWN"}
}

func TestRetirementUsesFixedBoundedIsolatedService(t *testing.T) {
	args := retirementTestArgs()
	unit, command, err := retirementServiceArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	unit2, command2, err := retirementServiceArgs(args)
	if err != nil || unit != unit2 || !reflect.DeepEqual(command, command2) {
		t.Fatal("same exact request lost its stable unit identity")
	}
	joined := strings.Join(command, "\n")
	for _, property := range []string{"--wait", "--pipe", "RuntimeMaxSec=45s", "MemoryMax=128M", "TasksMax=16", "NoNewPrivileges=yes", "PrivateNetwork=yes", "ProtectSystem=strict", "ProtectHome=yes", "ReadOnlyPaths=/var/lib/mcpshka/state.db", "ReadWritePaths=/var/lib/mcpshka /var/lib/mcpshka-release-gates /var/lib/mcpshka-executor /run/mcpshka-self-deploy.lock", "AmbientCapabilities=CAP_SETUID", "StateDirectory=mcpshka-deploy-fences", "RestrictAddressFamilies=AF_UNIX"} {
		if !strings.Contains(joined, property) {
			t.Fatalf("required worker boundary missing: %s", property)
		}
	}
	if !strings.Contains(joined, "CapabilityBoundingSet=CAP_DAC_READ_SEARCH CAP_SETUID CAP_SETGID CAP_SYS_PTRACE") || strings.Contains(joined, "CAP_DAC_OVERRIDE") {
		t.Fatal("reader worker acquired broader write privileges")
	}
	program := -1
	for i, arg := range command {
		if arg == "/usr/local/libexec/mcpshka-broker" {
			program = i
		}
	}
	want := append([]string{"/usr/local/libexec/mcpshka-broker", "retirement-worker"}, args...)
	if program < 0 || !reflect.DeepEqual(command[program:], want) {
		t.Fatalf("fixed gate command changed: %q", command)
	}
	for _, arg := range command {
		if arg == "/usr/local/libexec/mcpshka-self-deploy" {
			t.Fatal("waiting systemd launcher would block the gate's legacy process scan")
		}
	}
	changed := retirementTestArgs()
	changed[4] = strings.Repeat("d", 64)
	other, _, err := retirementServiceArgs(changed)
	if err != nil || other == unit {
		t.Fatal("changed receipt reused the original unit")
	}
}

func TestRetirementRejectsUntrustedCommandShapes(t *testing.T) {
	for _, change := range []struct {
		index int
		value string
	}{{0, "other"}, {1, "../session"}, {2, "local-stdio"}, {3, "../deployment"}, {4, "bad"}, {6, ""}, {6, "bad\nreason"}, {6, "--property=User=other"}} {
		args := retirementTestArgs()
		args[change.index] = change.value
		if _, _, err := retirementServiceArgs(args); err == nil {
			t.Fatalf("untrusted retirement input accepted: %q", args)
		}
	}
	if _, _, err := retirementServiceArgs(nil); err == nil {
		t.Fatal("missing fixed request accepted")
	}
}
