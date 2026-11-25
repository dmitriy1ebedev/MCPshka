package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mcpshka/internal/policy"
)

func TestDetectGoGitProject(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	cmd.Env = policy.MinimalEnv(root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/mcpshka\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	out, err := New(p).Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Git || out.Root != root || out.Description != "example.test/mcpshka" {
		t.Fatalf("unexpected project: %+v", out)
	}
	foundGo := false
	for _, kind := range out.Kinds {
		if kind == "go" {
			foundGo = true
		}
	}
	if !foundGo {
		t.Fatalf("go project not detected: %+v", out)
	}
}

func TestPlanTestStrategies(t *testing.T) {
	t.Run("make target wins", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\t@echo ok\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanTest(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "make" || plan.Command != "make" || len(plan.Args) != 1 || plan.Args[0] != "test" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("node script", func(t *testing.T) {
		root := t.TempDir()
		body := []byte(`{"scripts":{"test":"node test.js"}}`)
		if err := os.WriteFile(filepath.Join(root, "package.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanTest(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "node" || plan.Command != "npm" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("go fallback", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanTest(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "go" || plan.Command != "go" || len(plan.Args) != 4 || plan.Args[0] != "test" || plan.Args[1] != "-p" || plan.Args[2] != "1" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("pytest markers", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte("[tool.pytest.ini_options]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanTest(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "python-pytest" || plan.Command != "python3" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("unittest fallback", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanTest(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "python-unittest" || plan.Command != "python3" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})
}

func TestPlanTestRejectsNodePlaceholder(t *testing.T) {
	root := t.TempDir()
	body := []byte(`{"scripts":{"test":"echo "Error: no test specified" && exit 1"}}`)
	if err := os.WriteFile(filepath.Join(root, "package.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	if _, err := New(p).PlanTest(root); err == nil {
		t.Fatal("placeholder npm test should not be treated as a real test strategy")
	}
}

func TestPlanBuildStrategies(t *testing.T) {
	t.Run("make build target wins", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\n\t@echo ok\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanBuild(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "make" || plan.Command != "make" || len(plan.Args) != 1 || plan.Args[0] != "build" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("node build script", func(t *testing.T) {
		root := t.TempDir()
		body := []byte(`{"scripts":{"build":"node build.js"}}`)
		if err := os.WriteFile(filepath.Join(root, "package.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanBuild(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "node" || plan.Command != "npm" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("go build", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanBuild(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "go" || plan.Command != "go" || len(plan.Args) != 5 || plan.Args[0] != "build" || plan.Args[1] != "-p" || plan.Args[2] != "1" || plan.Args[3] != "-buildvcs=false" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("rust build", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte("[package]\nname='x'\nversion='0.1.0'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanBuild(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "rust" || plan.Command != "cargo" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("reject unknown project", func(t *testing.T) {
		root := t.TempDir()
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		if _, err := New(p).PlanBuild(root); err == nil {
			t.Fatal("expected no safe build strategy")
		}
	})
}

func TestPlanVerifyStrategies(t *testing.T) {
	t.Run("make verify", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@printf ok\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanVerify(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "make" || plan.Command != "make" || len(plan.Args) != 1 || plan.Args[0] != "verify" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("node verify", func(t *testing.T) {
		root := t.TempDir()
		body := []byte("{\"scripts\":{\"verify\":\"node verify.js\"}}")
		if err := os.WriteFile(filepath.Join(root, "package.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		plan, err := New(p).PlanVerify(root)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != "node" || plan.Command != "npm" || len(plan.Args) != 3 || plan.Args[1] != "verify" {
			t.Fatalf("unexpected plan: %+v", plan)
		}
	})

	t.Run("reject implicit verification", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		if _, err := New(p).PlanVerify(root); err == nil {
			t.Fatal("expected missing explicit verification contract to be rejected")
		}
	})
}

func TestDetectPrefersExplicitProjectMarkersOverAncestorGit(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "go.mod"), []byte("module example.test/child\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{parent}, DeniedCommands: map[string]struct{}{}}
	out, err := New(p).Detect(child)
	if err != nil {
		t.Fatal(err)
	}
	if out.Root != child {
		t.Fatalf("root=%q want explicit marker root %q", out.Root, child)
	}
	if out.Description != "example.test/child" {
		t.Fatalf("description=%q", out.Description)
	}
}
