package rootbroker

import (
	"strconv"
	"strings"
	"testing"

	"mcpshka/internal/executor"
)

func TestExecutorContractRejectsCallerCommandAndPaths(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{{"launch", id, id}, {"status", id, id}, {"fence", id, id}} {
		if e := validateExecutor(args); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{"launch", id, id, "/bin/sh"}, {"launch", "../root", id}, {"fence", id, "1"}, {"issue", id, id}, {"launch", strings.ToUpper(id), id}} {
		if validateExecutor(args) == nil {
			t.Fatalf("unsafe contract accepted: %v", args)
		}
	}
	raw := strings.Join(executorUnitArgs(id, id), " ")
	if strings.Contains(raw, "ProtectKernelTunables=yes") {
		t.Fatal("outer proc masking prevents private sandbox proc mount")
	}
	for _, wanted := range []string{"--no-block", "Restart=no", "KillMode=control-group", "RestrictSUIDSGID=no", "NoNewPrivileges=yes", "BindPaths=/var/lib/mcpshka-executor/workspaces/" + id + ":/run/mcpshka-executor-workspace", "AmbientCapabilities=CAP_SETUID CAP_SETGID", "MemoryMax=512M", "CPUQuota=25%", "TasksMax=32", "ProtectControlGroups=yes", "executor-worker"} {
		if !strings.Contains(raw, wanted) {
			t.Fatalf("profile missing %s", wanted)
		}
	}
}

func TestCandidateRetirementContract(t *testing.T) {
	id, fingerprint := strings.Repeat("a", 32), strings.Repeat("b", 64)
	for _, pin := range []string{"", fingerprint} {
		if err := validateExecutor([]string{"candidate-retire", id, pin}); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateExecutor([]string{"candidate-publish", id}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"candidate-retire", "../candidate", fingerprint}, {"candidate-retire", id, "true"}, {"candidate-retire", id, fingerprint, "/usr/local/bin/other"}, {"candidate-publish", id, fingerprint}, {"candidate-publish", "../approval"}} {
		if validateExecutor(args) == nil {
			t.Fatalf("caller authority accepted: %v", args)
		}
	}
}

func TestIntegrationApprovalBrokerAcceptsOnlyPinnedReadonlyLookup(t *testing.T) {
	permit, approval := strings.Repeat("a", 32), strings.Repeat("b", 64)
	if err := validateExecutor([]string{"integration-status", permit, approval}); err != nil {
		t.Fatalf("fixed approval status unavailable: %v", err)
	}
	for _, args := range [][]string{{"integration-status", permit, approval, "true"}, {"integration-status", permit, "../state.db"}, {"integration-status", permit, strings.ToUpper(approval)}, {"integration-approve", permit, approval}, {"integration-execute", permit, approval}, {"integration-status", approval, approval}} {
		if validateExecutor(args) == nil {
			t.Fatalf("caller authority accepted: %v", args)
		}
	}
}

func TestIntegrationSupervisorBrokerRequiresFixedApprovalIdentities(t *testing.T) {
	permit, approval := strings.Repeat("a", 32), strings.Repeat("b", 64)
	for _, action := range []string{"integration-recovery-status", "integration-recover"} {
		if err := validateExecutor([]string{action, permit, approval}); err != nil {
			t.Fatalf("fixed supervisor action unavailable: %v", err)
		}
		for _, args := range [][]string{{action, permit, "true"}, {action, permit, "../state.db"}, {action, permit, approval, "123"}, {action, approval, approval}} {
			if validateExecutor(args) == nil {
				t.Fatalf("caller-supplied recovery authority accepted: %v", args)
			}
		}
	}
}

func TestStoppedReadonlyWorkerRecoveryContract(t *testing.T) {
	id := strings.Repeat("a", 32)
	if err := validateExecutor([]string{"canary-recover", id, id}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"canary-recover", "../permit", id}, {"canary-recover", id, id, "true"}, {"canary-recover", id, "123"}} {
		if validateExecutor(args) == nil {
			t.Fatalf("unbound recovery accepted: %v", args)
		}
	}
}
func TestColdProjectTestRuntimeBudget(t *testing.T) {
	id := strings.Repeat("a", 32)
	raw := strings.Join(projectTestUnitArgs(id, id), " ")
	for _, want := range []string{"RuntimeMaxSec=23400s", "MemoryMax=512M", "CPUQuota=25%", "TasksMax=32", "PrivateNetwork=yes", "KillMode=control-group", "Restart=no"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing runtime bound %s", want)
		}
	}
}

func TestLocalReleaseReadContract(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{{"release-receipt", id, id}, {"release-read", id, "controller", "0"}, {"release-read", id, "broker", "32768"}} {
		if err := validateExecutor(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"release-read", id, "../receipt.json", "0"}, {"release-read", id, "controller", "-1"}, {"release-read", id, "broker", strconv.Itoa(executor.MaxReleaseImage + 1)}, {"release-receipt", id, id, "true"}} {
		if validateExecutor(args) == nil {
			t.Fatal(args)
		}
	}
}
