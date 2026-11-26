package sandboxbind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeBindsFollowOwnerAllowlistOnly(t *testing.T) {
	base := t.TempDir()
	saved := sandboxReservedPaths
	sandboxReservedPaths = []string{"/workspace", "/usr"} // t.TempDir lives under /tmp
	defer func() { sandboxReservedPaths = saved }()
	venvs := filepath.Join(base, "var/lib/lane/venvs/r1")
	secret := filepath.Join(base, "var/lib/lane/state")
	for _, d := range []string{venvs, secret, filepath.Join(base, "opt/lane")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "opt/lane/venv-current")
	if err := os.Symlink(venvs, link); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(base, "opt/lane/evil")
	if err := os.Symlink(secret, evil); err != nil {
		t.Fatal(err)
	}
	allow := filepath.Join(base, "allow")
	if err := os.WriteFile(allow, []byte("# owner list\n"+filepath.Join(base, "opt/lane")+"\n"+filepath.Join(base, "var/lib/lane/venvs")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	binds, err := Resolve([]string{link}, allow)
	if err != nil || len(binds) != 1 || binds[0].Target != venvs {
		t.Fatal("allowlisted venv link not resolved", binds, err)
	}
	if _, err := Resolve([]string{evil}, allow); err == nil {
		t.Fatal("symlink escaping the allowlist accepted")
	}
	if _, err := Resolve([]string{secret}, allow); err == nil {
		t.Fatal("non-allowlisted path accepted")
	}
	if err := os.Chmod(allow, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve([]string{link}, allow); err == nil {
		t.Fatal("world-writable allowlist trusted")
	}
	args := strings.Join(SandboxArgs(binds, map[string]string{"LANE_TEST_PYTHON": link + "/bin/python"}, 5), " ")
	for _, want := range []string{"--ro-bind-fd 5 " + venvs, "--symlink " + venvs + " " + link, "--setenv LANE_TEST_PYTHON"} {
		if !strings.Contains(args, want) {
			t.Fatalf("sandbox args lack %q: %s", want, args)
		}
	}
	if ValidEnv(map[string]string{"PATH": "/x"}) || ValidEnv(map[string]string{"LD_PRELOAD": "x"}) || !ValidEnv(map[string]string{"LANE_TEST_PYTHON": "/x"}) {
		t.Fatal("env boundary")
	}
	if ValidBinds([]Bind{{Path: "/usr/x", Target: "/usr/x"}}) {
		t.Fatal("bind over sandbox system path accepted")
	}
}

// MCP-146: a project's own untracked data needs no owner allowlist entry, but
// .git, the repository root and links out of it stay closed.
func TestRuntimeBindsAllowTestedRepositoryData(t *testing.T) {
	base := t.TempDir()
	saved := sandboxReservedPaths
	sandboxReservedPaths = []string{"/workspace", "/usr"}
	defer func() { sandboxReservedPaths = saved }()
	repo := filepath.Join(base, "repos/nfsu2")
	engine := filepath.Join(repo, "private/engine")
	outside := filepath.Join(base, "secret")
	for _, d := range []string{engine, filepath.Join(repo, ".git/objects"), outside} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	escape := filepath.Join(repo, "private/escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "absent.allow")
	if binds, err := Resolve([]string{engine}, missing, repo); err != nil || len(binds) != 1 || binds[0].Target != engine {
		t.Fatal("repository data refused", binds, err)
	}
	for _, p := range []string{repo, filepath.Join(repo, ".git/objects"), escape, outside} {
		if _, err := Resolve([]string{p}, missing, repo); err == nil {
			t.Fatal("accepted", p)
		} else if !strings.Contains(err.Error(), "RUNTIME_PATH") {
			t.Fatal("refusal without a reason code", p, err)
		}
	}
	if _, err := Resolve([]string{engine}, missing); err == nil {
		t.Fatal("repository data accepted without the repository")
	}
}
