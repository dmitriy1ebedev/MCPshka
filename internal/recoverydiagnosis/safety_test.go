package recoverydiagnosis

import "testing"

func TestCausalRecoveryClassesPreserveSafety(t *testing.T) {
	evidence := Evidence{JobKnown: true, JobState: "failed", Authenticated: true, LeaseHeld: true, LegacyOwnerUnbound: true, HistoricalDeployUnknown: true, BrokerIncompatible: true, Log: "AUTH_OK but LEASE_BUSY legacy_owner_unbound historical_deploy_unknown broker_incompatible provider_quota_exhausted"}
	out := Diagnose(evidence)
	for _, code := range []string{"AUTH_OK_but_LEASE_BUSY", "legacy_owner_unbound", "historical_deploy_unknown", "broker_incompatible", "PROVIDER_QUOTA_EXHAUSTED"} {
		count := 0
		for _, f := range out.Findings {
			if f.Code == code {
				count++
				if len(f.Recovery) == 0 || len(f.DoNot) != 3 {
					t.Fatalf("unsafe plan %+v", f)
				}
				if code != "PROVIDER_QUOTA_EXHAUSTED" && f.Verification != "observed" {
					t.Fatalf("observed evidence weakened: %+v", f)
				}
			}
		}
		if count != 1 {
			t.Fatalf("%s count=%d: %+v", code, count, out)
		}
	}
	if out.RetryAllowed {
		t.Fatal("classification authorized replay")
	}
	reported := Diagnose(Evidence{JobKnown: true, JobState: "interaction_failed", Log: "legacy_owner_unbound broker_incompatible historical_deploy_unknown AUTH_OK but LEASE_BUSY"})
	for _, f := range reported.Findings {
		if f.Verification != "inferred_from_untrusted_output" {
			t.Fatal(f)
		}
	}
	successful := Diagnose(Evidence{JobKnown: true, JobState: "done", Log: evidence.Log})
	if len(successful.Findings) != 0 {
		t.Fatal("successful injected output trusted")
	}
}
