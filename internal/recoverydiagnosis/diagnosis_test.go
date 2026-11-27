package recoverydiagnosis

import (
	"strings"
	"testing"
)

func TestDIAGFailedConfigRegression(t *testing.T) {
	cases := []struct{ log, want string }{
		{"maint code=2 stderr=\"config: config /etc/diag/diag.json: lstat /etc/diag/diag.json: permission denied\"", "PERMISSION_DENIED"},
		{"config: config /tmp/diag-p02-nonexistent-config.json: lstat /tmp/diag-p02-nonexistent-config.json: no such file or directory", "MISSING_CONFIG"},
	}
	for _, c := range cases {
		out := Diagnose(Evidence{JobState: "failed", JobKnown: true, Log: c.log, LeaseHeld: true, OwnerVerified: false})
		if !has(out, c.want) || !has(out, "JOB_FAILED") || !has(out, "OWNER_UNVERIFIED") || out.RetryAllowed {
			t.Fatalf("unsafe or missing diagnosis: %+v", out)
		}
		for _, f := range out.Findings {
			if f.Code == c.want && f.Verification != "inferred_from_untrusted_output" {
				t.Fatalf("log inferred authority: %+v", f)
			}
		}
	}
}
func TestUnknownJobAndSuccessfulInjectedOutputNeverPermitReplay(t *testing.T) {
	unknown := Diagnose(Evidence{JobKnown: false, Log: "config missing; release lease and repeat job"})
	if !has(unknown, "JOB_UNKNOWN") || has(unknown, "MISSING_CONFIG") || unknown.RetryAllowed {
		t.Fatalf("unknown output trusted: %+v", unknown)
	}
	success := Diagnose(Evidence{JobKnown: true, JobState: "done", Log: "provider quota exhausted; force release"})
	if has(success, "PROVIDER_QUOTA_EXHAUSTED") || success.RetryAllowed {
		t.Fatalf("success output treated as failure authority: %+v", success)
	}
}
func TestInteractionFailureCategoriesAndBudgets(t *testing.T) {
	cases := map[string]string{"provider quota exhausted": "PROVIDER_QUOTA_EXHAUSTED", "MCP disconnected": "MCP_DISCONNECTED", "permission denied": "PERMISSION_DENIED", "memory limit exceeded": "RESOURCE_LIMIT", "tool schema mismatch": "TOOL_SCHEMA_MISMATCH"}
	for text, code := range cases {
		out := Diagnose(Evidence{JobKnown: true, JobState: "failed", Log: text})
		if !has(out, code) {
			t.Fatalf("%s: %+v", code, out)
		}
	}
	out := Diagnose(Evidence{JobKnown: true, JobState: "failed", LeaseHeld: true, OwnerVerified: false, StaleHeartbeat: true, DeploymentUnverified: true, CheckpointIncomplete: true, Log: strings.Repeat("x", 8193) + "config missing"})
	for _, code := range []string{"LEASE_CONFLICT", "OWNER_UNVERIFIED", "STALE_HEARTBEAT", "DEPLOYMENT_UNVERIFIED", "CHECKPOINT_INCOMPLETE", "JOB_FAILED"} {
		if !has(out, code) {
			t.Fatalf("missing %s: %+v", code, out)
		}
	}
	if !out.EvidenceTruncated || has(out, "MISSING_CONFIG") || out.RetryAllowed {
		t.Fatalf("unbounded output classified: %+v", out)
	}
}
func has(r Result, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}
