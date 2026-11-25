package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func moduleFixtureRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Module Test")
	git("config", "user.email", "module@example.invalid")
	for name, body := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0755
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "committed module")
	return repo, git("rev-parse", "HEAD")
}

// unsealForCleanup restores write permission on directories the profile
// sealed read-only, so TempDir removal succeeds.
func unsealForCleanup(root string) {
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0755)
		}
		return nil
	})
}

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModuleSnapshotCopiesSourceAndListedDependenciesOnly(t *testing.T) {
	files := map[string]string{
		"go.mod":                      "module module.test\n\ngo 1.21\n\nrequire example.com/Dep v1.2.3\n",
		"go.sum":                      "example.com/Dep v1.2.3 h1:abc=\nexample.com/Dep v1.2.3/go.mod h1:def=\nexample.com/absent v0.1.0 h1:ghi=\n",
		"cmd/tool/main.go":            "package main\nfunc main() {}\n",
		"internal/core/core.go":       "package core\n",
		"internal/core/core_test.go":  "package core\n",
		"packaging/native/deploy.sh":  "#!/bin/sh\n",
		"packaging/systemd/x.service": "[Unit]\n",
		"README.md":                   "root readme",
		"CHANGELOG.md":                "changelog",
		"BUGLOG.md":                   "buglog",
		"docs/README.md":              "included spelling gate doc",
		"docs/releases/test.md":       "included release docs",
		"docs/nested/secret.md":       "nested docs excluded",
		".env":                        "must not copy",
	}
	repo, commit := moduleFixtureRepo(t, files)
	cache := t.TempDir()
	writeFixture(t, cache, map[string]string{
		"example.com/!dep@v1.2.3/dep.go":                    "package dep\n",
		"cache/download/example.com/!dep/@v/v1.2.3.mod":     "module example.com/Dep\n",
		"cache/download/example.com/!dep/@v/v1.2.3.ziphash": "h1:abc=\n",
		"cache/download/example.com/!dep/@v/v1.2.3.zip":     "zip is never copied",
		"example.com/unlisted@v9.9.9/x.go":                  "package unlisted\n",
	})
	saved := moduleCacheSource
	moduleCacheSource = cache
	defer func() { moduleCacheSource = saved }()

	list, err := projectTestTree(context.Background(), repo, commit, moduleTestPackage)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	t.Cleanup(func() { unsealForCleanup(base) })
	digest, err := materializeProjectTestSource(context.Background(), repo, moduleTestPackage, base, list)
	if err != nil || !digestPattern.MatchString(digest) {
		t.Fatal("module snapshot", digest, err)
	}
	for _, name := range []string{"go.mod", "go.sum", "cmd/tool/main.go", "internal/core/core_test.go", "packaging/native/deploy.sh", "packaging/systemd/x.service",
		"README.md", "CHANGELOG.md", "BUGLOG.md", "docs/README.md", "docs/releases/test.md",
		".gomodcache/example.com/!dep@v1.2.3/dep.go", ".gomodcache/cache/download/example.com/!dep/@v/v1.2.3.mod", ".gomodcache/cache/download/example.com/!dep/@v/v1.2.3.ziphash"} {
		if _, err := os.Stat(filepath.Join(base, name)); err != nil {
			t.Fatal("expected module input missing", name, err)
		}
	}
	for _, name := range []string{"docs/nested/secret.md", ".env", ".gomodcache/cache/download/example.com/!dep/@v/v1.2.3.zip", ".gomodcache/example.com/unlisted@v9.9.9"} {
		if _, err := os.Stat(filepath.Join(base, name)); !os.IsNotExist(err) {
			t.Fatal("unexpected input copied", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(base, "packaging/native/deploy.sh")); err != nil || info.Mode().Perm() != 0444 {
		t.Fatal("committed executable helpers must be materialized read-only", info, err)
	}
	info, err := os.Stat(filepath.Join(base, ".gomodcache/example.com/!dep@v1.2.3"))
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("dependency directories must be sealed read-only", info, err)
	}
}

func TestModuleSnapshotRejectsExecutableSourceOutsidePackaging(t *testing.T) {
	repo, commit := moduleFixtureRepo(t, map[string]string{"go.mod": "module module.test\n\ngo 1.21\n", "internal/core/core.go": "package core\n", "internal/core/run.sh": "#!/bin/sh\n"})
	list, err := projectTestTree(context.Background(), repo, commit, moduleTestPackage)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	t.Cleanup(func() { unsealForCleanup(base) })
	if _, err := materializeProjectTestSource(context.Background(), repo, moduleTestPackage, base, list); err != ErrHold {
		t.Fatal("executable outside packaging accepted", err)
	}
}

func TestModuleSnapshotRejectsCacheSymlinksAndHostileGoSum(t *testing.T) {
	cache := t.TempDir()
	writeFixture(t, cache, map[string]string{"example.com/dep@v1.0.0/dep.go": "package dep\n"})
	if err := os.Symlink("/etc/passwd", filepath.Join(cache, "example.com/dep@v1.0.0/leak.go")); err != nil {
		t.Fatal(err)
	}
	saved := moduleCacheSource
	moduleCacheSource = cache
	defer func() { moduleCacheSource = saved }()
	repo, commit := moduleFixtureRepo(t, map[string]string{
		"go.mod":          "module m\n",
		"go.sum":          "example.com/dep v1.0.0 h1:x=\n",
		"internal/a/a.go": "package a\n",
	})
	list, err := projectTestTree(context.Background(), repo, commit, moduleTestPackage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializeProjectTestSource(context.Background(), repo, moduleTestPackage, t.TempDir(), list); err == nil {
		t.Fatal("symlink inside the module cache was copied")
	}
	for _, sum := range []string{"../../etc v1.0.0 h1:x=\n", "example.com/dep ../v1 h1:x=\n", "example.com/dep v1.0.0\n", "/abs v1.0.0 h1:x=\n"} {
		base := t.TempDir()
		writeFixture(t, base, map[string]string{"go.sum": sum})
		root, err := os.OpenRoot(base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := goSumModules(root); err == nil {
			t.Fatalf("hostile go.sum accepted: %q", sum)
		}
		root.Close()
	}
}

func TestModuleProfileRunsWholeModuleOfflineWithDiskScratch(t *testing.T) {
	spec := RunSpec{Profile: ModuleTestProfile, Command: append([]string(nil), moduleTestCommand...), WorkspaceDevice: 1, WorkspaceInode: 1}
	if spec.validate() != nil {
		t.Fatal("module profile rejected")
	}
	for _, cmd := range [][]string{{"go", "test", "./..."}, {"go", "test", "-p", "1", "-count=1", "./...", "-exec", "sh"}, {"make", "test"}} {
		bad := spec
		bad.Command = cmd
		if bad.validate() == nil {
			t.Fatal("module profile accepted foreign command", cmd)
		}
	}
	args := strings.Join(workerArgs("p", spec), " ")
	for _, want := range []string{"--ro-bind-fd 3 /workspace", "--bind-fd 4 /scratch", "GOMODCACHE /workspace/.gomodcache", "GOCACHE /scratch/go-cache", "TMPDIR /scratch", "GOPROXY off", "GOMAXPROCS 2", "--unshare-all", "--cap-drop ALL", "-- go test -p 1 -count=1 -timeout=45m ./..."} {
		if !strings.Contains(args, want) {
			t.Fatalf("module sandbox missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "--bind-fd 3") || strings.Count(args, " -- ") != 1 {
		t.Fatal("module sandbox must keep the workspace read-only and one command separator", args)
	}
}

func TestModuleScratchBusySerializesWholeModuleTests(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	if err := moduleScratchBusy(root, "self", now); err != nil {
		t.Fatal("empty scratch root reported busy", err)
	}
	writeFixture(t, root, map[string]string{"self/go-cache/x": "own build"})
	if err := os.MkdirAll(filepath.Join(root, "idle/go-cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := moduleScratchBusy(root, "self", now); err != nil {
		t.Fatal("own or idle scratch reported busy", err)
	}
	writeFixture(t, root, map[string]string{"other/go-cache/y": "running build"})
	if err := moduleScratchBusy(root, "self", now); !errors.Is(err, errModuleBusy) {
		t.Fatal("concurrent whole-module build not detected", err)
	}
	if err := moduleScratchBusy(root, "self", now.Add(3*time.Hour)); err != nil {
		t.Fatal("stale leftover blocked admission forever", err)
	}
}

func TestIntegrationEvidenceAcceptsOnlyExactTestProfiles(t *testing.T) {
	module := Manifest{Purpose: ModuleTestPurpose, Source: &SourceSnapshot{Package: moduleTestPackage}}
	spec := RunSpec{Profile: ModuleTestProfile, Command: append([]string(nil), moduleTestCommand...), WorkspaceDevice: 1, WorkspaceInode: 1}
	if !integrationTestRun(module, spec) {
		t.Fatal("whole-module evidence rejected")
	}
	single := Manifest{Purpose: "project-test-v1", Source: &SourceSnapshot{Package: "internal/probe"}}
	if !integrationTestRun(single, RunSpec{Profile: "project-test-v1", Command: []string{"go", "test", "-p", "1", "./internal/probe"}, WorkspaceDevice: 1, WorkspaceInode: 1}) {
		t.Fatal("historical single-package evidence rejected")
	}
	for _, m := range []Manifest{
		{Purpose: ModuleTestPurpose, Source: &SourceSnapshot{Package: "internal/probe"}},
		{Purpose: "project-recipe-test-v1", Source: &SourceSnapshot{Package: moduleTestPackage}},
		{Purpose: ModuleTestPurpose},
	} {
		if integrationTestRun(m, spec) {
			t.Fatal("mismatched evidence accepted", m.Purpose)
		}
	}
	if integrationTestRun(module, RunSpec{Profile: "project-test-v1", Command: spec.Command, WorkspaceDevice: 1, WorkspaceInode: 1}) {
		t.Fatal("module evidence accepted under another profile")
	}
	if !integrationTestPurpose(ModuleTestPurpose) || integrationTestPurpose("canary-v1") {
		t.Fatal("integration purposes")
	}
}

func TestModuleTestIdentityMatchesSnapshotDerivation(t *testing.T) {
	p, launch, err := ModuleTestIdentity(strings.Repeat("a", 32))
	want, wantLaunch := snapshotIdentity(ModuleTestPurpose, strings.Repeat("a", 32))
	if err != nil || p != want || launch != wantLaunch {
		t.Fatal("observer identity differs from preparation")
	}
	if _, _, err := ModuleTestIdentity("../x"); err == nil {
		t.Fatal("invalid bootstrap accepted")
	}
}
