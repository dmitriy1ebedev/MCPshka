package rootbroker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"mcpshka/internal/projectlease"
)

func fenceRepairTestPlan(path string) projectlease.InteractiveFenceRepairPlan {
	return projectlease.InteractiveFenceRepairPlan{Path: path, DirectoryDevice: 1, DirectoryInode: 2, FenceDevice: 1, FenceInode: 3, BeforeUID: 0, BeforeGID: 0, TargetUID: 994, TargetGID: 984, JournalID: strings.Repeat("a", 64)}
}

func TestInteractiveFenceRepairFixedScopeAndPlanDecoder(t *testing.T) {
	for _, path := range []string{legacyLeaseRepository, "/root/repos/diag", "/root/repos/lane", "/root/repos/kopilka"} {
		plan := fenceRepairTestPlan(path)
		raw, _ := json.Marshal(plan)
		got, err := decodeFenceRepairPlan(string(raw))
		if err != nil || got != plan {
			t.Fatal("fixed pinned plan rejected", err)
		}
		for _, action := range []string{"preview-interactive-fence", "repair-interactive-fence"} {
			value := path
			if action == "repair-interactive-fence" {
				value = string(raw)
			}
			if err := validateLegacyLeaseRequest([]string{action, strings.Repeat("b", 32), "owner", value}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, alter := range []func(*projectlease.InteractiveFenceRepairPlan){
		func(p *projectlease.InteractiveFenceRepairPlan) { p.Path = "/root/repos/mcpshka-controlled" },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.Path = "/root/repos" },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.Path = "/etc" },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.Path = "/root/repos/diag/../diag" },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.Path = "/root/repos/diag/sub" },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.BeforeUID = 994 },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.TargetUID = 0 },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.Authorization = "caller-grant" },
		func(p *projectlease.InteractiveFenceRepairPlan) { p.JournalID = "invalid" },
	} {
		p := fenceRepairTestPlan("/root/repos/diag")
		alter(&p)
		raw, _ := json.Marshal(p)
		if _, err := decodeFenceRepairPlan(string(raw)); err == nil {
			t.Fatal("untrusted repair plan admitted", p)
		}
	}
	raw, _ := json.Marshal(fenceRepairTestPlan("/root/repos/diag"))
	for _, bad := range []string{string(raw) + " {}", string(raw[:len(raw)-1]) + `,"unknown":true}`, strings.Repeat(" ", 4097)} {
		if _, err := decodeFenceRepairPlan(bad); err == nil {
			t.Fatal("malformed repair plan admitted")
		}
	}
	for _, owner := range []string{"", "local-stdio"} {
		if validateLegacyLeaseRequest([]string{"preview-interactive-fence", strings.Repeat("b", 32), owner, "/root/repos/diag"}) == nil {
			t.Fatal("unauthenticated owner admitted")
		}
	}
}

func TestInteractiveFenceRepairTransientUnitBoundary(t *testing.T) {
	raw, _ := json.Marshal(fenceRepairTestPlan("/root/repos/diag"))
	request := []string{"repair-interactive-fence", strings.Repeat("b", 32), "owner", string(raw)}
	unit, args, err := legacyLeaseServiceArgs(request)
	if err != nil {
		t.Fatal(err)
	}
	other, again, err := legacyLeaseServiceArgs(request)
	if err != nil || unit != other || !reflect.DeepEqual(args, again) {
		t.Fatal("unstable repair unit")
	}
	text := strings.Join(args, "\n")
	for _, required := range []string{"--wait", "--pipe", "RuntimeMaxSec=45s", "MemoryMax=128M", "CPUQuota=50%", "TasksMax=16", "KillMode=control-group", "NoNewPrivileges=yes", "PrivateNetwork=yes", "ProtectHome=tmpfs", "ProtectSystem=strict", "BindReadOnlyPaths=/root/repos", "ReadWritePaths=/var/lib/mcpshka /var/lib/mcpshka-executor", "CAP_CHOWN", "RestrictAddressFamilies=AF_UNIX", "RestrictNamespaces=yes"} {
		if !strings.Contains(text, required) {
			t.Fatal("missing transient boundary", required)
		}
	}
	for _, forbidden := range []string{"CAP_SYS_ADMIN", "ProtectHome=no", "ReadWritePaths=/root"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("unexpected transient authority", forbidden)
		}
	}
	want := append([]string{executorHelper, "lease-recovery-worker"}, request...)
	if !reflect.DeepEqual(args[len(args)-len(want):], want) {
		t.Fatal("untrusted worker entry")
	}
	_, legacy, err := legacyLeaseServiceArgs(leaseWorkerArgs("preview-legacy-lease"))
	if err != nil || strings.Contains(strings.Join(legacy, "\n"), "CAP_CHOWN") {
		t.Fatal("CHOWN leaked to legacy recovery")
	}
}
