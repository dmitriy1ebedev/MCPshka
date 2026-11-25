package project

import (
	"mcpshka/internal/policy"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlanTargetedGoTests(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module example.test/x\n", "Makefile": "test:\n\tfalse\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}})
	plan, err := s.PlanTargetedTest(root, []string{"./internal/x", "./internal/y/..."}, "^TestX$")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != "go" || !reflect.DeepEqual(plan.Args, []string{"test", "-p", "1", "-run", "^TestX$", "./internal/x", "./internal/y/..."}) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	for _, pkg := range []string{"", "-exec=sh", "/tmp/x", "../x", "./../x", "./x/../../y", "./x;id", "./x y", "./x\\y", "example.com/x", "./x//y"} {
		if _, err := s.PlanTargetedTest(root, []string{pkg}, ""); err == nil {
			t.Errorf("accepted %q", pkg)
		}
	}
	if _, err := s.PlanTargetedTest(root, nil, "["); err == nil {
		t.Fatal("accepted invalid regex")
	}
	plan, err = s.PlanTargetedTest(root, nil, "TestX")
	if err != nil || plan.Args[len(plan.Args)-1] != "./..." {
		t.Fatalf("run only: %+v %v", plan, err)
	}
	plan, err = s.PlanTargetedTest(root, nil, "")
	if err != nil || plan.Kind != "make" {
		t.Fatalf("default changed: %+v %v", plan, err)
	}
	if _, err := s.PlanTargetedTest(root, []string{"."}, ""); err != nil {
		t.Fatalf("root package: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlanTargetedTest(root, []string{"./outside"}, ""); err == nil {
		t.Fatal("accepted module escape via symlink")
	}
	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlanTargetedTest(root, []string{"./x"}, ""); err == nil {
		t.Fatal("accepted non-Go project")
	}
}
