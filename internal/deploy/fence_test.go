package deploy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFenceBindsOriginalReceiptAndInstalledRootHelpers(t *testing.T) {
	now := time.Unix(1792170759, 0)
	id := "1792159092900-1e327387d562"
	base := deploymentFence{AuthorizationSessionID: strings.Repeat("e", 32), Schema: "mcpshka.deployment-fence/v1", DeploymentID: id, ReceiptSHA256: strings.Repeat("a", 64), GateSHA256: strings.Repeat("b", 64), HelperSHA256: strings.Repeat("c", 64), ObservedCurrentSHA256: strings.Repeat("d", 64), FencedAt: now.Unix(), Reason: "Historical outcome UNKNOWN; permanently exclude future launch"}
	check := func(raw []byte) error {
		return validateFence(raw, id, base.ReceiptSHA256, base.GateSHA256, base.HelperSHA256, now)
	}
	raw, _ := json.Marshal(base)
	if err := check(raw); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*deploymentFence){
		func(f *deploymentFence) { f.DeploymentID = "1792159092900-abcdef123456" },
		func(f *deploymentFence) { f.ReceiptSHA256 = strings.Repeat("e", 64) },
		func(f *deploymentFence) { f.GateSHA256 = strings.Repeat("e", 64) },
		func(f *deploymentFence) { f.HelperSHA256 = strings.Repeat("e", 64) },
		func(f *deploymentFence) { f.FencedAt = now.Unix() + 1 },
		func(f *deploymentFence) { f.Reason = " " },
		func(f *deploymentFence) { f.Reason = "bad\nreason" },
		func(f *deploymentFence) { f.ObservedCurrentSHA256 = strings.Repeat("A", 64) },
	} {
		modified := base
		change(&modified)
		raw, _ := json.Marshal(modified)
		if check(raw) == nil {
			t.Fatalf("accepted mismatched fence %s", raw)
		}
	}
	for _, bad := range [][]byte{append(raw, []byte(` {}`)...), []byte(strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)), []byte(strings.Repeat("x", 8193))} {
		if check(bad) == nil {
			t.Fatal("invalid/duplicate/over-budget fence accepted")
		}
	}
}

func TestStatusCannotClaimFenceWithoutRootEvidence(t *testing.T) {
	now := time.Now().UTC()
	s, err := observeStatus(Status{DeploymentID: "1792159092900-1e327387d562", State: "accepted", StartedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), Fenced: true, Retired: true, Blocking: false, FenceSHA256: strings.Repeat("a", 64)}, now)
	if err != nil || !s.Blocking || s.Fenced || s.Retired || s.FenceSHA256 != "" {
		t.Fatalf("untrusted receipt bypassed fence verification: %+v %v", s, err)
	}
}

func TestVerifiedFenceKeepsHistoricalUnknownAndBindsPrivilegedCode(t *testing.T) {
	now := time.Now().UTC()
	id := "1792159092900-1e327387d562"
	raw := []byte(`{"deployment_id":"1792159092900-1e327387d562","state":"accepted"}`)
	original := string(raw)
	gate, helper := []byte("installed gate"), []byte("installed helper")
	fence, _ := json.Marshal(deploymentFence{Schema: "mcpshka.deployment-fence/v1", DeploymentID: id,
		AuthorizationSessionID: strings.Repeat("e", 32), ReceiptSHA256: digest(raw),
		GateSHA256: digest(gate), HelperSHA256: digest(helper), ObservedCurrentSHA256: strings.Repeat("d", 64),
		FencedAt: now.Unix(), Reason: "permanent retirement, historical outcome UNKNOWN"})
	s, err := observeStatus(Status{DeploymentID: id, State: "accepted", StartedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := withVerifiedFence(s, raw, fence, gate, helper, now)
	if err != nil || got.Blocking || !got.Fenced || got.State != "accepted" || !got.ReconciliationRequired || got.FenceSHA256 != digest(fence) || got.FinishedAt != "" || string(raw) != original {
		t.Fatalf("historical receipt or blocking disposition incorrect: %+v %v", got, err)
	}
	for _, changed := range []struct{ gate, helper []byte }{{nil, helper}, {gate, nil}, {[]byte("new gate"), helper}, {gate, []byte("new helper")}} {
		got, err := withVerifiedFence(s, raw, fence, changed.gate, changed.helper, now)
		if err == nil || !got.Blocking || got.Fenced {
			t.Fatalf("changed/missing privileged evidence escaped HOLD: %+v %v", got, err)
		}
	}
}

func TestRootFenceEvidenceRejectsAliasesAndUnsafeModes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fence")
	if err := os.WriteFile(p, []byte("fence"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := rootEvidence(p, 8192); err == nil {
		t.Fatal("writable evidence accepted")
	}
	alias := p + ".link"
	if err := os.Symlink(p, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := rootEvidence(alias, 8192); err == nil {
		t.Fatal("symlink evidence accepted")
	}
}

func TestBrokerHistoricalFenceProofRequiresExactSourceAndNoReplay(t *testing.T) {
	id := "1792159092900-1e327387d562"
	receipt := digest([]byte("old persisted receipt"))
	fence := digest([]byte("old root fence"))
	good := map[string]string{
		"schema":         "mcpshka.historical-fence-proof/v1",
		"state":          "VERIFIED_NONREPLAYABLE",
		"deployment_id":  id,
		"receipt_sha256": receipt,
		"fence_sha256":   fence,
		"upgrade_id":     "b738d46dd7322e50e3768769fcd360a9",
	}
	raw, _ := json.Marshal(good)
	if e := verifyHistoricalProof(raw, id, receipt, fence); e != nil {
		t.Fatalf("root proof rejected: %v", e)
	}
	for key, value := range map[string]string{
		"schema": "foreign", "state": "AVAILABLE", "deployment_id": "1792159092900-1e327387d563",
		"receipt_sha256": strings.Repeat("a", 64), "fence_sha256": strings.Repeat("b", 64),
		"upgrade_id": strings.Repeat("e", 32),
	} {
		changed := map[string]string{}
		for k, v := range good {
			changed[k] = v
		}
		changed[key] = value
		body, _ := json.Marshal(changed)
		if e := verifyHistoricalProof(body, id, receipt, fence); e == nil {
			t.Fatalf("accepted %s mismatch", key)
		}
	}
	for _, changed := range [][]byte{
		append(append([]byte{}, raw...), []byte("{}")...),
		[]byte(strings.Replace(string(raw), `"state":`, `"unknown":"yes","state":`, 1)),
		[]byte("{}"),
	} {
		if e := verifyHistoricalProof(changed, id, receipt, fence); e == nil {
			t.Fatal("accepted malformed root proof")
		}
	}
	if e := verifyHistoricalProof(raw, "1792159092900-1e327387d563", receipt, fence); e == nil {
		t.Fatal("foreign deployment accepted")
	}
}

func TestPermanentFenceProofSurvivesInstallerReplacement(t *testing.T) {
	id := "1792159092900-1e327387d562"
	receipt, fence := digest([]byte("original receipt")), digest([]byte("original fence"))
	good := map[string]string{"schema": "mcpshka.historical-fence-proof/v2", "state": "VERIFIED_NONREPLAYABLE", "deployment_id": id, "receipt_sha256": receipt, "fence_sha256": fence, "retirement_sha256": digest([]byte("root sealed retirement"))}
	raw, _ := json.Marshal(good)
	if err := verifyHistoricalProof(raw, id, receipt, fence); err != nil {
		t.Fatalf("permanent root proof rejected: %v", err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"state":`, `"state":"VERIFIED_NONREPLAYABLE","state":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"Schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"retirement_sha256":`, `"upgrade_id":`, 1)),
		append(append([]byte{}, raw...), []byte("{}")...),
	} {
		if verifyHistoricalProof(bad, id, receipt, fence) == nil {
			t.Fatal("malformed permanent proof accepted")
		}
	}
	if verifyHistoricalProof(raw, id, receipt, digest([]byte("changed fence"))) == nil {
		t.Fatal("changed original fence accepted")
	}
}

func TestInvalidPermanentRetirementCannotFallBackToMatchingInstallers(t *testing.T) {
	now := time.Now()
	id := "1792159092900-1e327387d562"
	raw, gate, helper := []byte("original receipt"), []byte("original gate"), []byte("original helper")
	fence, _ := json.Marshal(deploymentFence{Schema: "mcpshka.deployment-fence/v1", DeploymentID: id, AuthorizationSessionID: strings.Repeat("e", 32), ReceiptSHA256: digest(raw), GateSHA256: digest(gate), HelperSHA256: digest(helper), ObservedCurrentSHA256: strings.Repeat("d", 64), FencedAt: now.Unix(), Reason: "original retirement"})
	if err := validateFence(fence, id, digest(raw), digest(gate), digest(helper), now); err != nil {
		t.Fatal(err)
	}
	s, err := observeStatus(Status{DeploymentID: id, State: "accepted"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		schema string
		err    error
	}{
		{"", errors.New("corrupt root retirement: HOLD")},
		{"", errors.New("aliased root retirement: HOLD")},
		{"mcpshka.historical-fence-proof/v1", nil},
	} {
		got, err := permanentFenceStatus(s, fence, bad.schema, bad.err)
		if err == nil || !got.Blocking || got.Retired {
			t.Fatalf("existing retirement failed open with valid old installers: %+v %v", got, err)
		}
	}
	got, err := permanentFenceStatus(s, fence, "mcpshka.historical-fence-proof/v2", nil)
	if err != nil || got.Blocking || !got.Retired {
		t.Fatalf("sealed retirement rejected: %+v %v", got, err)
	}
}
