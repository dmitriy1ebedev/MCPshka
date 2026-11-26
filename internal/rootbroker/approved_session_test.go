package rootbroker

import (
	"strings"
	"testing"
	"time"
)

func TestSessionCoveredRunDropsKernelAndNetworkAdmin(t *testing.T) {
	if sessionSandbox(ApprovedResult{}) != nil {
		t.Fatal("Face ID run narrowed")
	}
	got := strings.Join(sessionSandbox(ApprovedResult{CoveredBy: strings.Repeat("a", 32)}), " ")
	for _, want := range []string{"~CAP_NET_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_MODULE", "ProtectKernelModules=yes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s: %s", want, got)
		}
	}
	if _, err := authorizeApprovedSession(strings.Repeat("b", 32), "short", "owner", time.Now()); err == nil {
		t.Fatal("bad session id accepted")
	}
}
