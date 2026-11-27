package releaseapply

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPruneCandidatesKeepsNewAndCurrent(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"mcpshka-0.17.4.54f2f7fa4d51.candidate", "mcpshka-0.17.4.54f2f7fa4d51.candidate.ci-receipt.json",
		"mcpshka-broker-0.17.4.73c6f970efc5.candidate",
		"mcpshka-0.18.0.5c22ea9a52b5.candidate", "mcpshka-broker-0.18.0.fca4244028b9.candidate",
		"mcpshka-0.19.0.aaaaaaaaaaaa.candidate", "mcpshka-coordinated-release-0.17.4.json", "notes.txt",
	}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	removed := PruneCandidates(dir, "0.19.0", "0.18.0")
	sort.Strings(removed)
	if strings.Join(removed, ",") != "mcpshka-0.17.4.54f2f7fa4d51.candidate,mcpshka-0.17.4.54f2f7fa4d51.candidate.ci-receipt.json,mcpshka-broker-0.17.4.73c6f970efc5.candidate" {
		t.Fatalf("removed %v", removed)
	}
	if got := PruneCandidates(dir); got != nil {
		t.Fatal("empty keep must remove nothing")
	}
	for _, kept := range []string{"mcpshka-0.18.0.5c22ea9a52b5.candidate", "mcpshka-0.19.0.aaaaaaaaaaaa.candidate", "mcpshka-coordinated-release-0.17.4.json", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Fatalf("%s removed", kept)
		}
	}
}
