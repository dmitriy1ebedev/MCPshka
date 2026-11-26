package filesystem

import (
	"path/filepath"
	"testing"
)

func TestStatPathMissingFoldersUnderAllowedRoot(t *testing.T) {
	s, root := testService(t)
	got, err := s.StatPath(filepath.Join(root, ".gitverse", "workflows", "ci.yml"))
	if err != nil || got.Type != "missing" {
		t.Fatalf("missing parents must stat as missing: %+v %v", got, err)
	}
	if _, err := s.StatPath(filepath.Join(t.TempDir(), "x", "y")); err == nil {
		t.Fatal("missing parents outside allowed roots must still fail")
	}
}
