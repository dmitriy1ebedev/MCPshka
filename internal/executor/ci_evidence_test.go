package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/cievidence"
)

const (
	ciCommit = "1111111111111111111111111111111111111111"
	ciTree   = "2222222222222222222222222222222222222222"
)

func passingCIEvidence() cievidence.Evidence {
	checks := map[string]string{}
	for _, name := range cievidence.RequiredChecks {
		checks[name] = "success"
	}
	return cievidence.Evidence{Schema: cievidence.Schema, Repository: cievidence.Repository, HeadSHA: ciCommit, Tree: ciTree, BaseSHA: strings.Repeat("3", 40), Checks: checks, CheckedAt: time.Now().UTC()}
}

func writeCIEvidence(t *testing.T, raw []byte) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ciEvidenceFile), raw, 0444); err != nil {
		t.Fatal(err)
	}
	digest, err := snapshotDigestLimited(dir, 2, ciEvidenceMaxBytes)
	if err != nil || !digestPattern.MatchString(digest) {
		t.Fatal("evidence workspace exceeds its own limits", digest, err)
	}
	return dir, digest
}

func TestCIEvidenceProfileAcceptsOnlyTheFixedVerifier(t *testing.T) {
	ok := RunSpec{Profile: CIEvidenceProfile, Command: append([]string(nil), ciEvidenceCommand...), WorkspaceDevice: 1, WorkspaceInode: 1}
	if ok.validate() != nil {
		t.Fatal("fixed CI verifier rejected")
	}
	for _, command := range [][]string{{"go", "test", "-p", "1", "./..."}, {"verify-ci-evidence", "../evidence.json"}, {"sh", "-c"}} {
		if (RunSpec{Profile: CIEvidenceProfile, Command: command, WorkspaceDevice: 1, WorkspaceInode: 1}).validate() == nil {
			t.Fatal("CI profile accepted another command", command)
		}
	}
	m := Manifest{Purpose: CIEvidencePurpose, Source: &SourceSnapshot{Repo: "/root/repos/x", Commit: ciCommit, Package: ciEvidencePackage, Digest: strings.Repeat("a", 64)}}
	if !integrationTestPurpose(m.Purpose) || !integrationTestRun(m, ok) {
		t.Fatal("approval rejects sealed CI evidence")
	}
	if m.Source.Package = ciEvidencePackageFor(cievidence.GitVerseProvider); m.Source.Package != "ci:gitverse" || !integrationTestRun(m, ok) {
		t.Fatal("GitVerse evidence is not labelled ci:gitverse or is rejected", m.Source.Package)
	}
	if ciEvidencePackageFor("github") != "ci:github" {
		t.Fatal("GitHub evidence label changed")
	}
	m.Source.Package = moduleTestPackage
	if integrationTestRun(m, ok) {
		t.Fatal("CI profile accepted for a module snapshot")
	}
}

func TestCIEvidenceWorkerVerdict(t *testing.T) {
	source := &SourceSnapshot{Commit: ciCommit}
	raw, _ := json.Marshal(passingCIEvidence())
	dir, _ := writeCIEvidence(t, raw)
	if code, out := verifyCIEvidenceWorkspace(dir, source); code != 0 || !strings.Contains(out, ciCommit) {
		t.Fatal("passing evidence failed", code, out)
	}
	if code, _ := verifyCIEvidenceWorkspace(dir, &SourceSnapshot{Commit: strings.Repeat("4", 40)}); code == 0 {
		t.Fatal("evidence accepted for another commit")
	}
	failed := passingCIEvidence()
	failed.Checks["checks (race)"] = "failure"
	raw, _ = json.Marshal(failed)
	dir, _ = writeCIEvidence(t, raw)
	if code, out := verifyCIEvidenceWorkspace(dir, source); code == 0 || !strings.Contains(out, "checks (race)") {
		t.Fatal("failed check accepted or not named", code, out)
	}
	for _, bad := range []string{`{"schema":"x"}`, `{}` + `{}`, `not json`, `{"schema":"mcpshka.ci-evidence/v1","extra":1}`} {
		dir, _ = writeCIEvidence(t, []byte(bad))
		if code, _ := verifyCIEvidenceWorkspace(dir, source); code == 0 {
			t.Fatal("malformed evidence accepted", bad)
		}
	}
	if code, _ := verifyCIEvidenceWorkspace(t.TempDir(), source); code == 0 {
		t.Fatal("missing evidence accepted")
	}
}
