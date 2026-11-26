package issues

import "testing"

func TestIncidentFingerprintUsesErrorCodeBeforeDynamicTitle(t *testing.T) {
	a := Issue{Title: "Attempt 123 failed", Project: "app", Body: "PROVIDER_QUOTA_EXHAUSTED"}
	b := Issue{Title: "Another worker failed", Project: "app", Body: "PROVIDER_QUOTA_EXHAUSTED reset later"}
	if fingerprint(a) != fingerprint(b) {
		t.Fatal("same error code split into unrelated incidents")
	}
	b.Body = "BROKER_CIRCUIT_OPEN"
	if fingerprint(a) == fingerprint(b) {
		t.Fatal("different errors collapsed")
	}
}
