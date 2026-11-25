package projectrelease

import (
	"context"
	"mcpshka/internal/pipeline"
	"strings"
	"testing"
)

func TestStepExpandsOnlyRecipeArtifactReferences(t *testing.T) {
	var argv []string
	w := fakeWorker(t, true, func(_ Step, a []string) ([]byte, error) { argv = a; return nil, nil })
	s := Step{Name: "install", Argv: []string{"/usr/bin/true", "{captured}", "{version}"}}
	_, err := w.step(context.Background(), s, map[string]string{"captured": "{artifact:out/untrusted}", "version": "1.6.0"}, nil)
	if err != nil || len(argv) != 3 || argv[1] != "{artifact:out/untrusted}" || argv[2] != "1.6.0" {
		t.Fatalf("argv=%q err=%v", argv, err)
	}
}

func TestMissingRecipeArtifactRunsNoCommand(t *testing.T) {
	previous := pipeline.Exports
	pipeline.Exports = t.TempDir()
	t.Cleanup(func() { pipeline.Exports = previous })
	ran := false
	w := fakeWorker(t, true, func(Step, []string) ([]byte, error) { ran = true; return nil, nil })
	_, err := w.step(context.Background(), Step{Name: "install", Argv: []string{"/usr/bin/true", "--file={artifact:out/missing}"}}, nil, nil)
	if ran || err == nil || !strings.Contains(err.Error(), "next_safe_action = project_pipeline path="+w.root+" stages=[package]") {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	for _, arg := range []string{"{artifact:}", "{artifact:../private}", "{artifact:out/app"} {
		if _, err := ExpandArtifacts(w.root, w.res.Commit, arg); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
}

func TestExternalRecipeRequiresReadyArtifact(t *testing.T) {
	good := Recipe{Steps: []Step{{Name: "install", Argv: []string{"/usr/bin/install", "-m", "0755", "{artifact:out/distributed/diag}", "/usr/local/bin/diag"}}}}
	if e := externalRecipe(good); e != nil {
		t.Fatal(e)
	}
	bad := good
	bad.Steps = append([]Step{{Name: "build", Argv: []string{"/usr/bin/make", "build"}}}, good.Steps...)
	if externalRecipe(bad) == nil {
		t.Fatal("host compilation allowed")
	}
	if externalRecipe(Recipe{Steps: []Step{{Name: "install", Argv: []string{"/usr/bin/install", "build/diag", "/usr/local/bin/diag"}}}}) == nil {
		t.Fatal("unverified local artifact allowed")
	}
}
