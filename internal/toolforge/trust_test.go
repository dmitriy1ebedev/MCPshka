package toolforge

import "testing"

// MCP-086: findings carry rule id, severity, confidence, evidence and a safe
// migration; text overlap never licenses merging different contract classes
// and rare emergency tools are not "unused".
func TestOverlapNeverMergesDifferentApprovalClass(t *testing.T) {
	desc := "restart the managed service unit with verification and rollback receipts"
	tools := []Tool{
		{Name: "svc_restart", Description: desc, HasContract: true, HasReady: true, Calls: 3},
		{Name: "svc_bounce", Description: desc, HasContract: false, HasReady: false, Calls: 2},
		{Name: "svc_rescue_start", Description: "start the break-glass rescue path", HasContract: true, HasReady: true, Calls: 0},
	}
	seen := map[string]Finding{}
	for _, f := range Audit(tools) {
		if f.RuleID == "" || f.Severity == "" || f.Confidence == "" || f.Evidence == "" || f.SafeMigration == "" || f.SourceWindow == "" {
			t.Fatalf("finding lacks trust fields: %+v", f)
		}
		seen[f.Kind+":"+f.Tools[0]] = f
	}
	ov, ok := seen["overlap:svc_bounce"]
	if !ok || ov.Confidence != "LOW" || ov.KnownException == "" {
		t.Fatalf("overlap across contract classes must be LOW with exception: %+v", ov)
	}
	if r := seen["no_readiness:svc_bounce"]; r.Evidence == "" || r.Severity != "info" {
		t.Fatalf("no_readiness is UNPROBED info, not broken: %+v", r)
	}
	if u := seen["unused:svc_rescue_start"]; u.KnownException == "" {
		t.Fatalf("emergency tool must be a known exception: %+v", u)
	}
}
