package cibinding

import (
	"strings"
	"testing"
)

// MCP-091: unreviewed or unverified bindings never run; publication only
// follows an observed PASS of the same commit; artifacts are per project+SHA.
func TestBindingGatesAndRunStates(t *testing.T) {
	b := Binding{Project: "quakaii", Provider: "gitverse", Repo: "owner/quakaii", Branch: "main", Workflow: "ci.yml"}
	if ok, why := b.Runnable(); ok || !strings.Contains(why, "reviewed") {
		t.Fatal(why)
	}
	b.OwnerReviewed, b.DryRunOK = true, true
	if ok, why := b.Runnable(); ok || !strings.Contains(why, "dispatch") {
		t.Fatal(why)
	}
	b.CanDispatch = true
	if ok, _ := b.Runnable(); !ok {
		t.Fatal("ready binding refused")
	}
	c := strings.Repeat("ab", 20)
	r := Run{Binding: b, Commit: c, State: Unverified}
	if _, err := r.Advance(Published, r); err == nil {
		t.Fatal("published without PASS")
	}
	var err error
	for _, s := range []string{Queued, Running, Pass, Published} {
		if r, err = r.Advance(s, Run{Binding: b, Commit: c, RunURL: "https://ci/1"}); err != nil {
			t.Fatal(s, err)
		}
	}
	other := r
	other.Commit = strings.Repeat("cd", 20)
	if _, err := (Run{Binding: b, Commit: c, State: Running}).Advance(Pass, other); err == nil {
		t.Fatal("PASS of another commit accepted")
	}
	if k, _ := ArtifactKey("quakaii", c); k != "ci/quakaii/"+c {
		t.Fatal(k)
	}
	if _, err := ArtifactKey("quakaii", "HEAD"); err == nil {
		t.Fatal("symbolic ref accepted")
	}
}
