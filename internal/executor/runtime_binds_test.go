package executor

import "testing"

// Runtime binds are admitted only by the recipe profile.
func TestRuntimeBindsOnlyInRecipeProfile(t *testing.T) {
	binds := []RuntimeBind{{Path: "/opt/lane/venv", Target: "/opt/lane/venv"}}
	s := RunSpec{Profile: "recipe-test-v1", Command: []string{"sh", "t.sh"}, TimeoutSeconds: 60, WorkspaceDevice: 1, WorkspaceInode: 1, Runtime: binds}
	if err := s.validate(); err != nil {
		t.Fatalf("recipe runtime rejected: %v", err)
	}
	s.Profile, s.TimeoutSeconds = "project-test-v1", 0
	if s.validate() == nil {
		t.Fatal("runtime admitted outside recipe profile")
	}
}
