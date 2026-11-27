package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluateStaleAndDone(t *testing.T) {
	Dir = t.TempDir()
	if err := Put(Record{Project: "/r/p", Gate: "build", Result: "PASS", Inputs: Inputs{Head: "a", Tree: "t"}, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	Put(Record{Project: "/r/p", Gate: "docker", Result: "PASS", Inputs: Inputs{Head: "a"}, Source: "test"})
	if _, done := Evaluate("/r/p", []string{"build", "docker"}, Inputs{Head: "a", Tree: "t"}); !done {
		t.Fatal("matching PASS evidence must be done")
	}
	states, done := Evaluate("/r/p", []string{"build", "docker", "device"}, Inputs{Head: "b", Tree: "t"})
	if done || states[0].State != "STALE" || states[2].State != "MISSING" {
		t.Fatalf("%+v", states)
	}
	if Put(Record{Project: "/r/p", Gate: "nope", Result: "PASS"}) == nil {
		t.Fatal("unknown gate accepted")
	}
}

func TestRequiredGates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ROADMAP.md")
	os.WriteFile(p, []byte("# R\n## P11 old\nGates: build\n## P12 docker smoke\ntext\n- **Gates:** build, docker, `device`\n### sub\n## P13\nGates: deploy\n"), 0o644)
	g, err := RequiredGates(p, "P12")
	if err != nil || len(g) != 3 || g[2] != "device" {
		t.Fatalf("%v %v", g, err)
	}
	if g, _ := RequiredGates(p, "P13"); len(g) != 1 || g[0] != "deploy" {
		t.Fatalf("%v", g)
	}
	if _, err := RequiredGates(p, "P99"); err == nil {
		t.Fatal("missing step accepted")
	}
}

func TestScopedStagesDoNotOverwriteEachOtherOrAcceptance(t *testing.T) {
	old := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = old })
	in := Inputs{Head: "h", Tree: "t", ConfigDigest: "c", ArtifactSHA: "sha"}
	for _, scope := range []string{"pipeline/test", "pipeline/lint"} {
		if err := Put(Record{Project: "/p", Scope: scope, Gate: "engineering", Result: "PASS", Inputs: in}); err != nil {
			t.Fatal(err)
		}
	}
	if _, done := Evaluate("/p", []string{"engineering"}, in); done {
		t.Fatal("individual stage closed acceptance")
	}
	Put(Record{Project: "/p", Scope: "pipeline/test", Gate: "engineering", Result: "FAIL", Inputs: in})
	if _, done := Evaluate("/p", []string{"engineering"}, in, "pipeline/lint"); !done {
		t.Fatal("lint overwritten by test")
	}
	if _, done := Evaluate("/p", []string{"engineering"}, in, "pipeline/test"); done {
		t.Fatal("revoked PASS reused")
	}
	in.ConfigDigest = "changed"
	states, _ := Evaluate("/p", []string{"engineering"}, in, "pipeline/lint")
	if states[0].State != "STALE" {
		t.Fatal(states)
	}
}
