package filesystem

import (
	"mcpshka/internal/policy"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveMkdirAndBatchBudget(t *testing.T) {
	root := t.TempDir()
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}})
	dir := filepath.Join(root, "a", "b")
	if _, err := s.Mkdir(dir); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("abcdef"), 0600)
	os.WriteFile(b, []byte("keep"), 0600)
	if _, err := s.Move(a, b, false); err == nil {
		t.Fatal("clobbered destination")
	}
	if _, err := s.Move(a, filepath.Join(dir, "c"), false); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReadMany([]Known{{Path: b}, {Path: filepath.Join(dir, "c")}}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out[0].Text)+len(out[1].Text) > 5 {
		t.Fatal("aggregate budget exceeded")
	}
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))
	if _, err := s.Mkdir(filepath.Join(root, "escape", "new")); err == nil {
		t.Fatal("escaped root")
	}
	if _, err := s.Move(b, filepath.Join(outside, "b"), false); err == nil {
		t.Fatal("escaped root")
	}
}
