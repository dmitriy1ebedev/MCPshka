package rootbroker

import (
	"mcpshka/internal/distributedgate"
	"strings"
	"testing"
)

func TestDistributedPrepareUsesIndependentNetworkedPermit(t *testing.T) {
	args := []string{"integration-distributed-prepare", strings.Repeat("a", 32), "owner", "/root/repos/mcpshka", strings.Repeat("b", 40)}
	if e := validateIntegrationAdmin(args); e != nil {
		t.Fatal(e)
	}
	_, cmd, e := integrationAdminServiceArgs(args)
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(cmd, " ")
	for _, s := range []string{"--no-block", "PrivateNetwork=no", "/var/lib/mcpshka/github/api_token", "/var/lib/mcpshka/moshub/api_token", "/var/lib/mcpshka/gitverse/api_token"} {
		if !strings.Contains(joined, s) {
			t.Fatal("missing", s)
		}
	}
	if DistributedEvidenceBootstrap(args[3], args[4], "1") == distributedgate.GitVerseEvidenceBootstrap(args[3], args[4], "1") {
		t.Fatal("permit collision")
	}
	if !preparesEvidence(args[0]) {
		t.Fatal("synchronous broker call")
	}
}
