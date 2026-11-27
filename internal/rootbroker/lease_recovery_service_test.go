package rootbroker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/projectlease"
)

func leaseWorkerArgs(action string) []string {
	return []string{action, strings.Repeat("a", 32), "owner", "/root/repos/mcpshka"}
}

func TestLegacyLeaseWorkerBoundaries(t *testing.T) {
	args := leaseWorkerArgs("preview-legacy-lease")
	unit, command, err := legacyLeaseServiceArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	again, same, err := legacyLeaseServiceArgs(args)
	if err != nil || unit != again || !reflect.DeepEqual(command, same) {
		t.Fatal("unstable request identity")
	}
	joined := strings.Join(command, "\n")
	for _, required := range []string{"--pipe", "--wait", "RuntimeMaxSec=45s", "MemoryMax=128M", "CPUQuota=50%", "TasksMax=16", "KillMode=control-group", "NoNewPrivileges=yes", "PrivateNetwork=yes", "ProtectHome=tmpfs", "ProtectSystem=strict", "BindReadOnlyPaths=/root/repos", "ReadWritePaths=/var/lib/mcpshka /var/lib/mcpshka-executor", "CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_SETUID CAP_SETGID CAP_SYS_PTRACE", "AmbientCapabilities=CAP_SETUID CAP_SETGID", "RestrictAddressFamilies=AF_UNIX"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing worker boundary: %s", required)
		}
	}
	if strings.Contains(joined, "CAP_SYS_ADMIN") || strings.Contains(joined, "ProtectHome=no") {
		t.Fatal("worker broadened authority")
	}
	want := append([]string{"/usr/local/libexec/mcpshka-broker", "lease-recovery-worker"}, args...)
	if len(command) < len(want) || !reflect.DeepEqual(command[len(command)-len(want):], want) {
		t.Fatal("arbitrary worker command")
	}
	args[0] = "status-legacy-lease"
	changed, _, err := legacyLeaseServiceArgs(args)
	if err != nil || changed == unit {
		t.Fatal("different action reused worker identity")
	}
}

func TestLegacyLeaseWorkerRejectsUntrustedScope(t *testing.T) {
	for _, item := range []struct {
		index int
		value string
	}{{0, "other"}, {1, "../session"}, {2, "local-stdio"}, {3, "/etc"}, {3, "/root/repos/mcpshka/../mcpshka"}, {3, "--property=ProtectHome=no"}} {
		args := leaseWorkerArgs("status-legacy-lease")
		args[item.index] = item.value
		if _, _, err := legacyLeaseServiceArgs(args); err == nil {
			t.Fatalf("untrusted request accepted: %q", args)
		}
	}
	if _, _, err := legacyLeaseServiceArgs(nil); err == nil {
		t.Fatal("empty request accepted")
	}
	p := projectlease.RootSealedRecovery{RootSealedRetirement: projectlease.RootSealedRetirement{Path: "/root/repos/mcpshka", FenceRoot: "/root/repos/mcpshka", GitCommonDir: "/root/repos/mcpshka/.git", JournalID: strings.Repeat("b", 64), LeaseID: "lease-id", Owner: "previous-owner", ExecutionID: strings.Repeat("c", 32), RecordSHA256: strings.Repeat("d", 64), SnapshotTree: strings.Repeat("e", 40)}, NewOwner: "operator-root:mcpshka-one-tree-" + strings.Repeat("b", 16)}
	raw, _ := json.Marshal(p)
	args := leaseWorkerArgs("recover-legacy-lease")
	args[3] = string(raw)
	if _, _, err := legacyLeaseServiceArgs(args); err != nil {
		t.Fatal(err)
	}
	p.GitCommonDir = "/etc/.git"
	raw, _ = json.Marshal(p)
	args[3] = string(raw)
	if _, _, err := legacyLeaseServiceArgs(args); err == nil {
		t.Fatal("foreign Git authority accepted")
	}
}

func TestStaleLeaseWorkerNestedScopesStayReadOnly(t *testing.T) {
	for _, path := range []string{"/root/repos/mcpshka", "/root/repos/mcpshka/bin/tier1-consolidated-wt/diag-validation-wt", "/root/repos/diag/bin/_release-0.1.1-verify"} {
		args := leaseWorkerArgs("preview-stale-lease")
		args[3] = path
		_, command, err := legacyLeaseServiceArgs(args)
		if err != nil {
			t.Fatalf("nested preview %s: %v", path, err)
		}
		for _, arg := range command {
			if strings.HasPrefix(arg, "--property=ReadWritePaths=") && arg != "--property=ReadWritePaths=/var/lib/mcpshka /var/lib/mcpshka-executor" {
				t.Fatal("worker write scope broadened", arg)
			}
		}
		if !strings.Contains(strings.Join(command, "\n"), "--property=BindReadOnlyPaths=/root/repos\n") {
			t.Fatal("repository evidence is not read-only")
		}
		p := projectlease.StaleReleasePlan{Path: path, LeaseID: "lease", Owner: "owner", RecordSHA256: strings.Repeat("a", 64), LastHeartbeat: time.Now().UTC()}
		raw, _ := json.Marshal(p)
		args[0], args[3] = "release-stale-lease", string(raw)
		if _, _, err := legacyLeaseServiceArgs(args); err != nil {
			t.Fatal("nested release refused", err)
		}
		if path != "/root/repos/mcpshka" && fenceRepairRepository(path) {
			t.Fatal("fence repair boundary broadened")
		}
	}
	for _, path := range []string{"/root/repos/diag/bin/../gone", "/root/repos/diag//gone", "/root/repos/diag/gone/", "/tmp/repos/diag/gone", "/root/repos/mcpshka-controlled/bin/gone", "/root/repos/diag/" + strings.Repeat("a/", 9) + "gone"} {
		args := leaseWorkerArgs("preview-stale-lease")
		args[3] = path
		if validateLegacyLeaseRequest(args) == nil {
			t.Fatalf("unsafe preview accepted: %s", path)
		}
		p := projectlease.StaleReleasePlan{Path: path, LeaseID: "lease", Owner: "owner", RecordSHA256: strings.Repeat("a", 64), LastHeartbeat: time.Now().UTC()}
		raw, _ := json.Marshal(p)
		if _, err := decodeStaleReleasePlan(string(raw)); err == nil {
			t.Fatalf("unsafe release accepted: %s", path)
		}
	}
}
