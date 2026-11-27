package distributedci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPlanUsesCommittedInventory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("fixture is owned by test user; source reader intentionally drops root")
	}
	dir := t.TempDir()
	put := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0644); e != nil {
			t.Fatal(e)
		}
	}
	put("go.mod", "module mcpshka\ngo 1.26\n")
	put("packaging/ci/plan.json", "{\"weights\":{}}")
	for _, pkg := range []string{"a", "b", "c", "d"} {
		put(pkg+"/main.go", "package "+pkg+"\n")
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return string(b)
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "fixture")
	commit := git("rev-parse", "HEAD")
	commit = commit[:40]
	put("uncommitted/main.go", "package uncommitted\n")
	p, e := LoadPlan(context.Background(), dir, commit)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Packages) != 4 {
		t.Fatal("dirty package entered plan", p.Packages)
	}

	put(".mcpshka/opencore.json", `{"exclude":["docs"]}`)
	put("docs/private.md", "internal notes")
	for _, name := range []string{".github/workflows/distributed.yml", ".gitverse/workflows/distributed.yml", ".gitlab-ci.yml", ".sourcecraft/ci.yaml"} {
		put(name, "fixture")
	}
	git("add", ".mcpshka", "docs", ".github", ".gitverse", ".gitlab-ci.yml", ".sourcecraft")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "export fixture")
	exportCommit := git("rev-parse", "HEAD")[:40]
	public, e := LoadPublicPlan(context.Background(), dir, exportCommit)
	if e != nil {
		t.Fatal(e)
	}
	canonical, e := CanonicalTree(context.Background(), dir, exportCommit)
	if e != nil || !digestRE.MatchString(public.Digest) || public.Tree == canonical || len(public.Packages) != len(p.Packages) || ShardRef(public, 0) != "ci/"+exportCommit[:12]+"/shard-0" {
		t.Fatalf("bad export plan %+v, %v", public, e)
	}
}

func TestSourceCommandSuppliesMissingServiceCache(t *testing.T) {
	if os.Geteuid() != 994 {
		t.Skip("H96 service-specific environment fallback")
	}
	t.Setenv("HOME", "/nonexistent")
	t.Setenv("GOPATH", "/nonexistent")
	t.Setenv("GOCACHE", "")
	raw, e := command(context.Background(), t.TempDir(), "go", "env", "GOCACHE", "GOPATH")
	if e != nil || !strings.Contains(string(raw), "/var/lib/mcpshka/.cache/go-build") || !strings.Contains(string(raw), "/var/lib/mcpshka/go") {
		t.Fatal(string(raw), e)
	}
}
