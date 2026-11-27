package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"

	"mcpshka/internal/executor"
)

func TestStalePermitWorkerFixedScopeAndPinnedPlan(t *testing.T) {
	args := []string{"preview-stale-permits", strings.Repeat("a", 32), "owner:test", "all"}
	if !leaseRecoveryReadOnly(args[0]) {
		t.Fatal("preview classified as mutation")
	}
	_, command, err := legacyLeaseServiceArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(command, "\n"), "--property=User=root") {
		t.Fatal("not a root worker")
	}
	args[3] = "/tmp/authority.db"
	if validateLegacyLeaseRequest(args) == nil {
		t.Fatal("caller authority accepted")
	}
	plan := executor.StalePermitPlan{Permits: []executor.StalePermitPin{{Pin: executor.Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}, RecordSHA256: strings.Repeat("d", 64)}}}
	raw, _ := json.Marshal(plan)
	args[0] = "reconcile-stale-permits"
	args[3] = string(raw)
	if leaseRecoveryReadOnly(args[0]) {
		t.Fatal("apply classified as read-only")
	}
	if validateLegacyLeaseRequest(args) != nil {
		t.Fatal("valid plan refused")
	}
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), "record_sha256", "untrusted_hash", 1), `{"permits":[]}`, strings.Replace(string(raw), strings.Repeat("d", 64), "invalid", 1)} {
		args[3] = bad
		if validateLegacyLeaseRequest(args) == nil {
			t.Fatal("untrusted plan accepted", bad)
		}
	}
	args[3] = string(raw)
	args[2] = "local-stdio"
	if validateLegacyLeaseRequest(args) == nil {
		t.Fatal("unbound local owner accepted")
	}
	args[2] = "owner:test"
	args[1] = "invalid"
	if validateLegacyLeaseRequest(args) == nil {
		t.Fatal("invalid admin session accepted")
	}
}
