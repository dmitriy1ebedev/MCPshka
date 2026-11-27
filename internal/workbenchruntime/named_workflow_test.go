package workbenchruntime

import (
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"reflect"
	"testing"
)

func TestNamedWorkflowUsesPinnedIsolatedRuntime(t *testing.T) {
	p := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "fixture", Workflows: map[string]projectprofile.Workflow{
		"fast": {Command: "python3", Args: []string{"packaging/native/test-plan.py", "FAST", "--run"}, Env: map[string]string{"FIXTURE": "fast"}},
		"test": {Command: "make", Args: []string{"test"}, Env: map[string]string{"FIXTURE": "test"}},
	}}
	r := &Runtime{resolve: func(string) (projectprofile.Resolution, error) {
		return projectprofile.Resolution{Source: "registered", Profile: p}, nil
	}}
	digest := projectprofile.ProfileDigest(p)
	want := []string{"python3", "packaging/native/test-plan.py", "FAST", "--run"}
	argv, ok := r.sealedArgv("mcpshka-project-workflow", []string{"fast", digest, "head"}, "/root/repos/fixture")
	if !ok || !reflect.DeepEqual(argv, want) {
		t.Fatalf("registered check fell through host interpreter policy: %v %v", argv, ok)
	}
	for _, args := range [][]string{{"fast"}, {"fast", "changed", "head"}, {"missing", digest, "head"}, {"fast", digest, "head", "extra"}} {
		if r.Match("mcpshka-project-workflow", args, "/root/repos/fixture") {
			t.Fatal("untrusted named workflow admitted", args)
		}
	}
	r.policy = policy.Default()
	r.policy.DeniedCommands["python3"] = struct{}{}
	if r.Match(WorkflowCommand, []string{"fast", digest, "head"}, "/root/repos/fixture") {
		t.Fatal("sealed route bypassed explicit command denial")
	}
	if r.Match("python3", want[1:], "/root/repos/fixture") {
		t.Fatal("raw host interpreter admitted")
	}
}
