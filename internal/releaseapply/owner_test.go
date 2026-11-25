package releaseapply

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerReleaseKeepsExistingApplyEvidence(t *testing.T) {
	old := ResultDir
	ResultDir = t.TempDir()
	t.Cleanup(func() { ResultDir = old })
	const version = "0.17.3"
	if err := ownerReleaseUnstarted(version); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(ResultDir, version+".json")
	before := []byte(`{"state":"HOLD","reason":"original outcome unknown"}`)
	if err := os.WriteFile(file, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ownerReleaseUnstarted(version); err == nil {
		t.Fatal("owner path allowed to replace an existing apply report")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(before) {
		t.Fatalf("original report changed: %v", err)
	}
}

func TestOwnerReleaseRequiresRemoteArm64Evidence(t *testing.T) {
	for _, tc := range []struct {
		kind, arm64 string
		want        bool
	}{
		{"module", "", false},
		{"github", "skipped", false},
		{"github", "success", true},
	} {
		if got := ownerReleaseHasArm64(tc.kind, tc.arm64); got != tc.want {
			t.Errorf("%s/%s: got %t, want %t", tc.kind, tc.arm64, got, tc.want)
		}
	}
}
