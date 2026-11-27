package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeReleasePlan(t *testing.T, path, version, sum string) {
	t.Helper()
	body := `{"schema":"mcpshka.release-plan/v2","version":"` + version + `","candidate_sha256":"` + sum + `"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestCleanupRemovesOnlyReceiptedCandidatesOlderThanRollback(t *testing.T) {
	old := installedBrokerPaths
	installedBrokerPaths = nil
	t.Cleanup(func() { installedBrokerPaths = old })
	s, repo, unreceipted, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.166")
	bin := filepath.Join(repo, "bin")

	superseded := filepath.Join(bin, "mcpshka-0.1.150.aaaaaaaaaaaa.candidate")
	writeVersionBinary(t, superseded, "0.1.150")
	writeReleasePlan(t, superseded+".release-plan.json", "0.1.150", fileSHA(t, superseded))

	newer := filepath.Join(bin, "mcpshka-0.1.170.bbbbbbbbbbbb.candidate")
	writeVersionBinary(t, newer, "0.1.170")
	writeReleasePlan(t, newer+".release-plan.json", "0.1.170", fileSHA(t, newer))

	// A receipt claiming an old version cannot release the production binary.
	body, err := os.ReadFile(s.target)
	if err != nil {
		t.Fatal(err)
	}
	production := filepath.Join(bin, "mcpshka-0.1.140.cccccccccccc.candidate")
	if err := os.WriteFile(production, body, 0700); err != nil {
		t.Fatal(err)
	}
	writeReleasePlan(t, production+".release-plan.json", "0.1.140", fileSHA(t, production))

	plan, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Remove, []string{filepath.Base(superseded)}) || plan.RemoveBytes <= 0 || plan.HoldReason != "" {
		t.Fatalf("plan: %+v", plan)
	}
	for _, keep := range []string{newer, production, unreceipted} {
		if !slices.Contains(plan.Keep, filepath.Base(keep)) {
			t.Fatalf("%s not kept: %+v", keep, plan)
		}
	}
	applied, err := s.CleanupCandidates(context.Background(), repo, plan.Fingerprint)
	if err != nil || !applied.Applied {
		t.Fatal(applied, err)
	}
	if _, err := os.Stat(superseded); !os.IsNotExist(err) {
		t.Fatal("superseded candidate not removed", err)
	}
	for _, keep := range []string{newer, production, unreceipted, superseded + ".release-plan.json"} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatal("kept file removed", keep, err)
		}
	}
}

func TestCleanupRemovalStopsWhenCandidateChanged(t *testing.T) {
	old := installedBrokerPaths
	installedBrokerPaths = nil
	t.Cleanup(func() { installedBrokerPaths = old })
	s, repo, _, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.166")
	superseded := filepath.Join(repo, "bin", "mcpshka-0.1.150.aaaaaaaaaaaa.candidate")
	writeVersionBinary(t, superseded, "0.1.150")
	writeReleasePlan(t, superseded+".release-plan.json", "0.1.150", fileSHA(t, superseded))
	plan, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil || len(plan.Remove) != 1 {
		t.Fatal(plan, err)
	}
	writeVersionBinary(t, superseded, "0.1.151")
	if _, err := s.CleanupCandidates(context.Background(), repo, plan.Fingerprint); err == nil {
		t.Fatal("changed candidate removed under an old fingerprint")
	}
	if _, err := os.Stat(superseded); err != nil {
		t.Fatal(err)
	}
}

func TestVersionBefore(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.1.150", "0.1.166", true}, {"0.1.166", "0.1.166", false}, {"0.1.170", "0.1.166", false}, {"0.1.a.23", "0.1.166", false}, {"0.1.9", "0.1.10", true}} {
		if versionBefore(c.a, c.b) != c.want {
			t.Fatal(c)
		}
	}
}
