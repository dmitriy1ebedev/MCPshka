package project

import (
	"mcpshka/internal/policy"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomaticGoPlanUsesDiffAndReverseDependencies(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	write("go.mod", "module example.test/m\n\ngo 1.23\n")
	write("a/a.go", "package a\nconst X=1\n")
	write("b/b.go", "package b\nimport \"example.test/m/a\"\nvar X=a.X\n")
	write("c/c.go", "package c\n")
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@x", "commit", "-qm", "seed")
	git("checkout", "-qb", "task")
	write("a/a.go", "package a\nconst X=2\n")
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}})
	plan, err := s.PlanTargetedTest(root, nil, "")
	if err != nil || strings.Join(plan.Args, " ") != "test -p 1 ./a ./b" || !strings.Contains(plan.Reason, "обратные зависимости") {
		t.Fatal(plan, err)
	}
	write("Makefile", "test:\n\ttrue\n")
	plan, err = s.PlanTargetedTest(root, nil, "")
	if err != nil || plan.Command != "make" {
		t.Fatal("mandatory workflow skipped", plan, err)
	}
}

func TestGoTestNoTimeout(t *testing.T) {
	got := GoTestNoTimeout(TestPlan{Command: "go", Args: []string{"test", "-p", "1", "./x"}})
	if strings.Join(got, " ") != "test -timeout=0 -p 1 ./x" {
		t.Fatal(got)
	}
	keep := []string{"test", "-timeout=5m", "./x"}
	if got := GoTestNoTimeout(TestPlan{Command: "go", Args: keep}); strings.Join(got, " ") != strings.Join(keep, " ") {
		t.Fatal("explicit timeout replaced:", got)
	}
	if got := GoTestNoTimeout(TestPlan{Command: "make", Args: []string{"test"}}); len(got) != 1 {
		t.Fatal("non-go plan changed:", got)
	}
}
