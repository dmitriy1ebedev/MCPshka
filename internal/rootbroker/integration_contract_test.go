package rootbroker

import (
	"mcpshka/internal/executor"
	"strings"
	"testing"
)

func TestIntegrationBrokerDispatchFixedIdentityContract(t *testing.T) {
	permit, approval := strings.Repeat("a", 32), strings.Repeat("b", 64)
	for _, action := range []string{"integration-launch", "integration-worker-status", "integration-worker-fence", "integration-worker-recover", "integration-abort"} {
		if err := validateExecutor([]string{action, permit, approval}); err != nil {
			t.Fatal(action, err)
		}
		for _, args := range [][]string{{action, permit, "../authority"}, {action, approval, approval}, {action, permit, approval, "/bin/sh"}, {action, permit, strings.ToUpper(approval)}} {
			if validateExecutor(args) == nil {
				t.Fatal("caller path or authority accepted", args)
			}
		}
	}
}
func TestIntegrationBrokerUnitGrantsOnlyRootBirthStoreAndBoundedRuntime(t *testing.T) {
	m := executor.IntegrationLaunch{Pin: executor.Pin{PermitID: strings.Repeat("a", 32)}, LaunchID: strings.Repeat("b", 32), Claim: executor.IntegrationClaim{Repository: executor.RepositoryIdentity{CommonDir: executor.ProtectedRepositoryRoot + "/" + strings.Repeat("c", 32) + ".git"}}}
	args := integrationUnitArgs(m)
	raw := strings.Join(args, " ")
	for _, want := range []string{"Restart=no", "KillMode=control-group", "RuntimeMaxSec=780s", "MemoryMax=512M", "CPUQuota=25%", "TasksMax=32", "NoNewPrivileges=yes", "PrivateNetwork=yes", "ProtectSystem=strict", "ProtectHome=read-only", "ReadWritePaths=" + executor.AuthorityRoot + " " + m.Claim.Repository.CommonDir, "InaccessiblePaths=-" + DefaultSocketPath, "--property=AmbientCapabilities=CAP_SETUID CAP_SETGID ", "executor-integration-worker " + m.Pin.PermitID + " " + m.LaunchID} {
		if !strings.Contains(raw, want) {
			t.Fatal("missing unit boundary", want)
		}
	}
	// Ambient carries only the identity switch for source reads, nothing wider.
	if strings.Count(raw, "AmbientCapabilities") != 1 || strings.Contains(raw, "CAP_SYS_ADMIN") || strings.Contains(raw, "BindPaths") || strings.Contains(raw, "/bin/sh") {
		t.Fatal("unexpected privilege or command grant")
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--property=ReadWritePaths=") && arg != "--property=ReadWritePaths="+executor.AuthorityRoot+" "+m.Claim.Repository.CommonDir {
			t.Fatal("broad writable grant", arg)
		}
	}
}
