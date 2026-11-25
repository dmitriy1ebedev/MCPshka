package rootbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPermanentRetirementReopensWithoutInstallerImages(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	fence := []byte("immutable original root fence")
	args := []string{legacyFenceID, historicSHA([]byte("original receipt")), historicSHA(fence)}
	r := permanentRetirement{Schema: "mcpshka.permanent-retirement/v1", State: "NONREPLAYABLE_OUTCOME_UNKNOWN", DeploymentID: args[0], ReceiptSHA256: args[1], FenceSHA256: args[2], AuthorizationSessionID: strings.Repeat("a", 32), RetiredAt: now.Unix()}
	path := retirementPath(dir, args[0])
	if err := writeRootOnly(path, r, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, args[0]+".json"), fence, 0600); err != nil {
		t.Fatal(err)
	}
	read := func(path string, limit int64) ([]byte, error) {
		return historicalRootReadOwned(path, limit, uint32(os.Geteuid()))
	}
	// Every probe opens real files anew; there are no archived/current installers.
	for i := 0; i < 2; i++ {
		raw, err := readPermanentRetirement(dir, args, now, read)
		if err != nil {
			t.Fatal(err)
		}
		var proof historicalFenceProof
		if json.Unmarshal(raw, &proof) != nil || proof.Schema != "mcpshka.historical-fence-proof/v2" || proof.UpgradeID != "" || !validHash(proof.RetirementSHA256) {
			t.Fatalf("invalid permanent proof: %s", raw)
		}
	}
	original, _ := os.ReadFile(path)
	if writeRootOnly(path, r, true) == nil {
		t.Fatal("immutable record overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("retirement changed")
	}
	if err := os.WriteFile(filepath.Join(dir, args[0]+".json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPermanentRetirement(dir, args, now, read); err == nil {
		t.Fatal("changed original fence accepted")
	}
}

func TestPermanentRetirementRejectsCorruptionAliasesAndPartialWrite(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	fence := []byte("original fence")
	args := []string{legacyFenceID, historicSHA([]byte("receipt")), historicSHA(fence)}
	r := permanentRetirement{Schema: "mcpshka.permanent-retirement/v1", State: "NONREPLAYABLE_OUTCOME_UNKNOWN", DeploymentID: args[0], ReceiptSHA256: args[1], FenceSHA256: args[2], AuthorizationSessionID: strings.Repeat("a", 32), RetiredAt: now.Unix()}
	good, _ := json.Marshal(r)
	path := retirementPath(dir, args[0])
	read := func(path string, limit int64) ([]byte, error) {
		return historicalRootReadOwned(path, limit, uint32(os.Geteuid()))
	}
	if err := os.WriteFile(filepath.Join(dir, args[0]+".json"), fence, 0600); err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{[]byte("{"), append(append([]byte{}, good...), []byte("{}")...), []byte(strings.Replace(string(good), `"state":`, `"state":"NONREPLAYABLE_OUTCOME_UNKNOWN","state":`, 1)), []byte(strings.Replace(string(good), `"schema":`, `"Schema":`, 1))}
	for _, change := range []func(*permanentRetirement){
		func(r *permanentRetirement) { r.ReceiptSHA256 = strings.Repeat("b", 64) },
		func(r *permanentRetirement) { r.FenceSHA256 = strings.Repeat("b", 64) },
		func(r *permanentRetirement) { r.DeploymentID = "1792159092900-aaaaaaaaaaaa" },
		func(r *permanentRetirement) { r.AuthorizationSessionID = "bad" },
		func(r *permanentRetirement) { r.State = "SUCCEEDED" },
		func(r *permanentRetirement) { r.RetiredAt = now.Unix() + 1 },
	} {
		x := r
		change(&x)
		raw, _ := json.Marshal(x)
		bad = append(bad, raw)
	}
	for _, raw := range bad {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPermanentRetirement(dir, args, now, read); err == nil {
			t.Fatalf("bad retirement accepted: %s", raw)
		}
		if writeRootOnly(path, r, true) == nil {
			t.Fatal("partial/corrupt retirement silently replaced")
		}
	}
	if err := os.WriteFile(path, good, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := readPermanentRetirement(dir, args, now, read); err == nil {
		t.Fatal("writable root record accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+".hardlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := readPermanentRetirement(dir, args, now, read); err == nil {
		t.Fatal("aliased root record accepted")
	}
}
