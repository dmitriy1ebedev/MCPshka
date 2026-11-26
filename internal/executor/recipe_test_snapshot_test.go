package executor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecipeSnapshotUsesCommittedMakeAndFixtures(t *testing.T) {
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
	git("config", "user.name", "Recipe Test")
	git("config", "user.email", "recipe@example.invalid")
	files := map[string]string{"go.mod": "module recipe.test\n\ngo 1.21\n", "Makefile": "test:\n\tCGO_ENABLED=0 go test ./...\n", "cmd/diag/main.go": "package main\nfunc main() {}\n", "internal/report/schema_test.go": "package report\n", "schemas/report-v1alpha1.schema.json": "{}", ".env": "must not copy", "docs/note": "must not copy"}
	for name, body := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "committed recipe")
	commit := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte("dirty work"), 0644); err != nil {
		t.Fatal(err)
	}
	list, err := projectTestTree(context.Background(), repo, commit, recipeTestPackage)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if _, err := materializeProjectTestSource(context.Background(), repo, recipeTestPackage, base, list); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		got, err := os.ReadFile(filepath.Join(base, name))
		if name == ".env" || strings.HasPrefix(name, "docs/") {
			if !os.IsNotExist(err) {
				t.Fatal("unrelated source copied", name, err)
			}
		} else if err != nil || string(got) != body {
			t.Fatal("committed recipe input missing or changed", name, err)
		}
	}
	var withoutSchema []byte
	for _, entry := range strings.Split(string(list), "\x00") {
		if entry != "" && !strings.HasSuffix(entry, "\tschemas/report-v1alpha1.schema.json") {
			withoutSchema = append(withoutSchema, []byte(entry+"\x00")...)
		}
	}
	if _, err := materializeProjectTestSource(context.Background(), repo, recipeTestPackage, t.TempDir(), withoutSchema); err == nil {
		t.Fatal("required schema missing but accepted")
	}
}

func TestRecipeProfileKeepsMakeInsideReadonlySandbox(t *testing.T) {
	s := RunSpec{TimeoutSeconds: 300, Profile: "recipe-test-v1", WorkspaceDevice: 1, WorkspaceInode: 2, Command: []string{"make", "test"}}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(workerArgs("", s), " ")
	for _, want := range []string{"--ro-bind-fd 3 /workspace", "--size 67108864 --tmpfs /tmp", "--size 16777216 --tmpfs /run", "--bind-fd 4 /scratch", "GOCACHE /scratch/go-cache", "GOMAXPROCS 2", "GOMEMLIMIT 1200MiB", "-- make test"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, command := range [][]string{{"make", "build"}, {"make", "test", "GO=/evil"}, {"go", "test", "-p", "1", "./internal/probe"}, {"python3", "-I", "-c", "print(1)"}} {
		if RecipeCommandMatches(recipeTestPackage, command) {
			t.Fatalf("Make snapshot accepted %v", command)
		}
		if !RecipeCommandMatches(treeTestPackage, command) {
			t.Fatalf("registered tree argv rejected %v", command)
		}
	}
	for _, command := range [][]string{{"/usr/bin/make", "test"}, {"../x"}, {"./../x"}, {"./.git/hooks/x"}, {"sh", "a\nb"}, {""}, {}} {
		s.Command = command
		if s.validate() == nil || RecipeCommandMatches(treeTestPackage, command) {
			t.Fatalf("unsafe argv accepted %q", command)
		}
	}
	s.Command = []string{"./tests/run_safe.sh"}
	if err := s.validate(); err != nil {
		t.Fatal("committed script rejected", err)
	}
	if args := strings.Join(workerArgs("", s), " "); !strings.Contains(args, "PYTHONDONTWRITEBYTECODE 1") || !strings.HasSuffix(args, "-- ./tests/run_safe.sh") {
		t.Fatal("tree sandbox environment missing", args)
	}
	s.Command = []string{"make", "test"}
	s.TimeoutSeconds = 0
	for _, profile := range []string{"project-test-v1", "isolated-v1"} {
		s.Profile = profile
		if s.validate() == nil {
			t.Fatalf("Make admitted to %s", profile)
		}
	}
}

func TestTreeSnapshotPathAndModeBoundary(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "tests/run_safe.sh", "lane/core.py"} {
		if !treeTestPath(name) {
			t.Fatalf("tracked file rejected: %s", name)
		}
	}
	for _, name := range []string{".git/config", "a/.git/x", "../x", "/etc/passwd", "a/../b", "a\\b", "."} {
		if treeTestPath(name) {
			t.Fatalf("outside input accepted: %s", name)
		}
	}
	for _, mode := range []string{"120000", "160000"} {
		bad := []byte(mode + " blob " + strings.Repeat("a", 40) + "\tlink\x00")
		if _, err := materializeProjectTestSource(context.Background(), t.TempDir(), treeTestPackage, t.TempDir(), bad); err == nil {
			t.Fatal("link or submodule accepted", mode)
		}
	}
}

func TestRecipeSnapshotPathBoundary(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum", "Makefile", "cmd/diag/main.go", "internal/report/schema_test.go", "schemas/report-v1alpha1.schema.json"} {
		if !recipeTestPath(name) {
			t.Fatalf("required input rejected: %s", name)
		}
	}
	for _, name := range []string{"/etc/passwd", "../go.mod", "cmd/../secret", "cmd/.git/config", "docs/file", ".env", "AGENTS.md", "schemas\\secret"} {
		if recipeTestPath(name) {
			t.Fatalf("outside input accepted: %s", name)
		}
	}
	for _, mode := range []string{"120000", "100755", "160000"} {
		list := []byte(mode + " blob " + strings.Repeat("a", 40) + "\tMakefile\x00")
		if _, err := materializeProjectTestSource(context.Background(), t.TempDir(), recipeTestPackage, t.TempDir(), list); err == nil {
			t.Fatal("unsafe mode accepted", mode)
		}
	}
	base := t.TempDir()
	if _, err := materializeProjectTestSource(context.Background(), t.TempDir(), recipeTestPackage, base, nil); err == nil {
		t.Fatal("empty recipe accepted")
	}
	if entries, _ := os.ReadDir(base); len(entries) != 0 {
		t.Fatal("invalid snapshot wrote files")
	}
}

func TestRecipeConstructorRejectsUnpinnedSources(t *testing.T) {
	for _, in := range [][3]string{{"bad", "/root/repos/diag", strings.Repeat("a", 40)}, {strings.Repeat("b", 32), "/tmp/repo", strings.Repeat("a", 40)}, {strings.Repeat("b", 32), "/root/repos/diag", "HEAD"}, {strings.Repeat("b", 32), "/root/repos/diag/../diag", strings.Repeat("a", 40)}} {
		if _, err := PrepareRecipeTest(context.Background(), in[0], in[1], in[2]); err == nil {
			t.Fatalf("unpinned recipe accepted %v", in)
		}
	}
	s := &SourceSnapshot{Repo: "/root/repos/diag", Commit: strings.Repeat("a", 40), Package: recipeTestPackage, Digest: strings.Repeat("b", 64)}
	if !validSource(s) {
		t.Fatal("recipe source rejected")
	}
	s.Package = "make:build"
	if validSource(s) {
		t.Fatal("arbitrary recipe accepted")
	}
}

func TestRecipeTimeoutBoundaries(t *testing.T) {
	s := RunSpec{Profile: "recipe-test-v1", Command: []string{"make", "test"}, WorkspaceDevice: 1, WorkspaceInode: 1}
	for _, timeout := range []int64{-1, 0, 21601} {
		if validRecipeTimeout(timeout) {
			t.Fatal("invalid recipe constructor timeout accepted", timeout)
		}
		s.TimeoutSeconds = timeout
		if s.validate() == nil {
			t.Fatal("invalid sealed timeout", timeout)
		}
		if _, err := PrepareRecipeTest(context.Background(), strings.Repeat("a", 32), "/root/repos/diag", strings.Repeat("b", 40), timeout); err == nil {
			t.Fatal("invalid constructor timeout", timeout)
		}
	}
	for _, timeout := range []int64{1, 300, 1800, 1944, 21600} {
		if !validRecipeTimeout(timeout) {
			t.Fatal("valid recipe constructor timeout rejected", timeout)
		}
		s.TimeoutSeconds = timeout
		if err := s.validate(); err != nil {
			t.Fatal(err)
		}
	}
	s.Profile = "project-test-v1"
	s.Command = []string{"go", "test", "-p", "1", "./internal/probe"}
	if s.validate() == nil {
		t.Fatal("old profile accepted caller TTL")
	}
	if _, err := PrepareRecipeTest(context.Background(), "bad", "/root/repos/diag", "HEAD", 1, 2); err == nil {
		t.Fatal("multiple TTLs accepted")
	}
}

func TestRegisteredGoTreeSealsListedOfflineModules(t *testing.T) {
	depMod := "module example.com/dep\n"
	fileHash := sha256.Sum256([]byte(depMod))
	modHash := sha256.Sum256([]byte(fmt.Sprintf("%x  go.mod\n", fileHash)))
	modSum := "h1:" + base64.StdEncoding.EncodeToString(modHash[:])
	repo, commit := moduleFixtureRepo(t, map[string]string{
		"go.mod":  "module example.test\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n",
		"go.sum":  "example.com/dep v1.0.0 h1:abc=\nexample.com/dep v1.0.0/go.mod " + modSum + "\n",
		"main.go": "package main\nfunc main(){}\n",
	})
	cache := t.TempDir()
	writeFixture(t, cache, map[string]string{"example.com/dep@v1.0.0/dep.go": "package dep\n", "cache/download/example.com/dep/@v/v1.0.0.mod": depMod, "cache/download/example.com/dep/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`, "cache/download/example.com/dep/@v/v1.0.0.ziphash": "h1:abc=\n"})
	saved := moduleCacheSource
	moduleCacheSource = cache
	t.Cleanup(func() { moduleCacheSource = saved })
	list, err := projectTestTree(context.Background(), repo, commit, treeTestPackage)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	t.Cleanup(func() { unsealForCleanup(base) })
	if _, err = materializeProjectTestSource(context.Background(), repo, treeTestPackage, base, list); err != nil {
		t.Fatal(err)
	}
	dep := filepath.Join(base, ".gomodcache/example.com/dep@v1.0.0/dep.go")
	if info, err := os.Stat(dep); err != nil || info.Mode().Perm() != 0444 {
		t.Fatal("dependency missing/unsealed", info, err)
	}
	args := strings.Join(workerArgs("", RunSpec{Profile: "recipe-test-v1", Command: []string{"go", "test", "./..."}}), " ")
	if !strings.Contains(args, "GOMODCACHE /workspace/.gomodcache") || !strings.Contains(args, "GOPROXY off") {
		t.Fatal(args)
	}
	// Use Go itself to verify that the copied cache resolves the dependency
	// with networking disabled, rather than only checking filenames.
	cmd := exec.Command("go", "list", "-m", "all")
	cmd.Dir = base
	cmd.Env = append(os.Environ(), "GOMODCACHE="+filepath.Join(base, moduleDepsDir), "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOCACHE="+t.TempDir())
	raw, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(raw), "example.com/dep v1.0.0") {
		t.Fatalf("offline Go resolution failed: %v %s", err, raw)
	}
}
