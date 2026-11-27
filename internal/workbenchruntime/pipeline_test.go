package workbenchruntime

import (
	"encoding/base64"
	"encoding/json"
	"mcpshka/internal/recipecontract"
	"reflect"
	"testing"

	"mcpshka/internal/projectprofile"
)

func TestPipelineRuntimeMatchesOnlyPinnedProfileWorkflow(t *testing.T) {
	p := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "fixture", Workflows: map[string]projectprofile.Workflow{"verify": {Command: "make", Args: []string{"verify"}, TimeoutSeconds: 60, OutputLimitBytes: 4096}}}
	r := &Runtime{resolve: func(string) (projectprofile.Resolution, error) {
		return projectprofile.Resolution{Source: "registered", Profile: p}, nil
	}}
	digest := projectprofile.ProfileDigest(p)
	argv, ok := r.sealedArgv(StageCommand, []string{"verify", digest, "head"}, "/root/repos/fixture")
	if !ok || !reflect.DeepEqual(argv, []string{"make", "verify"}) {
		t.Fatalf("profile not admitted: %v %v", argv, ok)
	}
	for _, args := range [][]string{{"verify"}, {"verify", "changed", "head"}, {"deploy", digest, "head"}, {"verify", digest, "head", "make"}} {
		if r.Match(StageCommand, args, "/root/repos/fixture") {
			t.Fatalf("untrusted stage admitted: %v", args)
		}
	}
	if r.Match("make", []string{"verify"}, "/root/repos/fixture") {
		t.Fatal("host make verify admitted")
	}
}

func TestSealedGoTestUsesManagedTimeout(t *testing.T) {
	for _, tc := range []struct {
		stage string
		args  []string
		want  []string
	}{
		{"test", []string{"test", "-p", "1", "./..."}, []string{"go", "test", "-timeout=0", "-p", "1", "./..."}},
		{"test", []string{"test", "-timeout=4m", "./..."}, []string{"go", "test", "-timeout=4m", "./..."}},
		{"verify", []string{"test", "./..."}, []string{"go", "test", "./..."}},
	} {
		profile := projectprofile.Profile{
			Schema: projectprofile.SchemaV1, Name: "fixture",
			Workflows: map[string]projectprofile.Workflow{tc.stage: {Command: "go", Args: tc.args}},
		}
		r := &Runtime{resolve: func(string) (projectprofile.Resolution, error) {
			return projectprofile.Resolution{Source: "registered", Profile: profile}, nil
		}}
		digest := projectprofile.ProfileDigest(profile)
		for _, command := range []string{StageCommand, WorkflowCommand} {
			got, ok := r.sealedArgv(command, []string{tc.stage, digest, "head"}, "/root/repos/fixture")
			if !ok || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s %s: got %v (admitted %t), want %v", command, tc.stage, got, ok, tc.want)
			}
		}
	}
}

func TestManagedProjectTestMatchesNormalizedProfileArgs(t *testing.T) {
	workflow := projectprofile.Workflow{Command: "go", Args: []string{"test", "-p", "1", "./..."}}
	r := &Runtime{resolve: func(string) (projectprofile.Resolution, error) {
		return projectprofile.Resolution{Source: "registered", Profile: projectprofile.Profile{
			Workflows: map[string]projectprofile.Workflow{"test": workflow},
		}}, nil
	}}
	want := []string{"go", "test", "-timeout=0", "-p", "1", "./..."}
	for _, args := range [][]string{workflow.Args, want[1:]} {
		got, ok := r.sealedArgv("go", args, "/root/repos/fixture")
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("go %v: got %v (admitted %t), want %v", args, got, ok, want)
		}
	}
	if r.Match("go", []string{"test", "-timeout=0", "./internal/projectprofile"}, "/root/repos/fixture") {
		t.Fatal("targeted tests must not be admitted as full sealed profile tests")
	}
}

func TestPipelineBindingPinsExtrasAndArtifacts(t *testing.T) {
	b, _ := testBinding(t, 60)
	b.Stage = "package"
	b.Argv = []string{"make", "build"}
	b.Artifacts = []string{"app.ipk"}
	b.Bootstrap = bootstrap(b)
	b.Pin, b.Launch, _ = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	raw, _ := json.Marshal(b)
	if _, err := decode(raw, false); err != nil {
		t.Fatal(err)
	}
	args := b.recipeArgs("prepare-recipe")
	encoded := args[5][len("spec="):]
	extra, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var spec recipecontract.RecipeExtras
	if json.Unmarshal(extra, &spec) != nil || spec.Stage != "package" || !reflect.DeepEqual(spec.Artifacts, b.Artifacts) {
		t.Fatalf("lost pipeline fields: %s", extra)
	}
	b.Artifacts = []string{"changed.ipk"}
	raw, _ = json.Marshal(b)
	if _, err := decode(raw, false); err == nil {
		t.Fatal("artifact binding tampering accepted")
	}
}
