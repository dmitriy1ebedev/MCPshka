package opencore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "x")
	return dir, run("rev-parse", "HEAD")
}

const policy = `{"repository":"o/r","exclude":["docs/history","docs/ISSUE_*.md"],"fixtures":["fake_test.go"]}`

func TestExportDropsPrivatePathsAndReplacesIdentifiers(t *testing.T) {
	repo, commit := gitRepo(t, map[string]string{
		ConfigPath:                 policy,
		"main.go":                  "package main // bind 192.168.77.10, not 192.168.0.1730 nor 10.192.168.0.173; peer host.tail9.ts.net\n",
		"docs/history/x.md":        "секрет владельца",
		"docs/ISSUE_REGISTRY_1.md": "карточки",
		"docs/README.md":           "MAC 02:00:00:00:00:46",
		"fake_test.go":             "var k = \"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890\"\n",
	})
	rules := []Rule{{From: "192.168.77.10", To: "192.168.77.10"}, {From: "tail9.ts.net", To: "example.ts.net"}, {From: "02:00:00:00:00:46", To: "02:00:00:00:00:46"}}
	dest := filepath.Join(t.TempDir(), "out")
	m, err := Export(context.Background(), repo, commit, dest, rules)
	if err != nil {
		t.Fatalf("%v %+v", err, m.Blocking)
	}
	if m.Files != 3 || m.Excluded != 3 || m.Replaced != 2 {
		t.Fatalf("manifest %+v", m)
	}
	src, _ := os.ReadFile(filepath.Join(dest, "main.go"))
	if want := "bind 192.168.77.10, not 192.168.0.1730 nor 10.192.168.0.173; peer host.example.ts.net"; !strings.Contains(string(src), want) {
		t.Fatalf("main.go: %s", src)
	}
	if _, err := os.Stat(filepath.Join(dest, "docs/history")); !os.IsNotExist(err) {
		t.Fatal("excluded folder exported")
	}
	again := filepath.Join(t.TempDir(), "again")
	if m2, err := Export(context.Background(), repo, commit, again, rules); err != nil || m2.Digest != m.Digest {
		t.Fatalf("export is not deterministic: %v %s %s", err, m2.Digest, m.Digest)
	}
}

func TestExportOverlayReplacesPublicCI(t *testing.T) {
	repo, commit := gitRepo(t, map[string]string{
		ConfigPath:       `{"repository":"o/r","overlay":"packaging/opencore/public"}`,
		".gitlab-ci.yml": "private snapshots only\n",
		"packaging/opencore/public/.gitlab-ci.yml":          "public main\n",
		"packaging/opencore/public/.github/workflows/a.yml": "on: push\n",
		"LICENSE": "Apache License\n",
	})
	dest := filepath.Join(t.TempDir(), "out")
	m, err := Export(context.Background(), repo, commit, dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	ci, _ := os.ReadFile(filepath.Join(dest, ".gitlab-ci.yml"))
	if string(ci) != "public main\n" || m.Files != 3 {
		t.Fatalf("%q %+v", ci, m)
	}
	for _, p := range []string{".github/workflows/a.yml", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "packaging/opencore")); !os.IsNotExist(err) {
		t.Fatal("overlay source exported")
	}
}

func TestExportBlocksRealSecretsAndLeftovers(t *testing.T) {
	repo, commit := gitRepo(t, map[string]string{
		ConfigPath: policy,
		"cfg.go":   "var k = \"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890\"\n",
	})
	dest := filepath.Join(t.TempDir(), "out")
	m, err := Export(context.Background(), repo, commit, dest, nil)
	if !errors.Is(err, ErrBlocked) || len(m.Blocking) != 1 || m.Blocking[0].File != "cfg.go" {
		t.Fatalf("secret must block: %v %+v", err, m.Blocking)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("blocked export left a tree behind")
	}
	if _, err := Export(context.Background(), repo, strings.Repeat("0", 40), filepath.Join(t.TempDir(), "x"), nil); err == nil {
		t.Fatal("unknown commit exported")
	}
}

func TestPlaceholdersKeepTheAddressClass(t *testing.T) {
	got := map[string]string{}
	for _, r := range placeholders([]string{"192.168.77.10", "127.0.0.1", "172.17.0.1", "100.77.0.10", "fd7a:115c:a1e0::77:10"}, []string{"100.77.1.11"}) {
		got[r.From] = r.To
	}
	want := map[string]string{"192.168.77.10": "192.168.77.10", "100.77.0.10": "100.77.0.10", "fd7a:115c:a1e0::77:10": "fd7a:115c:a1e0::77:10", "100.77.1.11": "100.77.1.10"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s → %s, want %s (%v)", k, got[k], v, got)
		}
	}
}
