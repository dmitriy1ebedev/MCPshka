package executor

import (
	"strings"
	"testing"
)

func TestPreparablePackageAdmitsGitVerseEvidence(t *testing.T) {
	for _, pkg := range []string{ciEvidencePackage, ciGitVersePackage, moduleTestPackage} {
		if !preparablePackage(pkg) {
			t.Fatalf("%s refused (BUG-20261023-73)", pkg)
		}
	}
	if preparablePackage("ci:unknown") {
		t.Fatal("unknown evidence label admitted")
	}
}

// The sealed GitVerse manifest could not be reopened either: validSource had
// its own copy of the label list (BUG-20261023-73, second half).
func TestValidSourceAdmitsGitVerseEvidence(t *testing.T) {
	s := &SourceSnapshot{Repo: "/root/repos/mcpshka", Commit: strings.Repeat("a", 40), Package: ciGitVersePackage, Digest: strings.Repeat("b", 64)}
	if !validSource(s) {
		t.Fatal("GitVerse evidence manifest refused")
	}
}
