package projectprofile

import (
	"reflect"
	"testing"
)

func TestPipelineStagesAndArtifactPaths(t *testing.T) {
	for _, stages := range [][]string{{"wat"}, {"test", "build"}, {"test", "test"}, {"all", "test"}} {
		if _, err := StageSelection(stages); err == nil {
			t.Fatalf("accepted %v", stages)
		}
	}
	got, err := StageSelection(nil)
	if err != nil || !reflect.DeepEqual(got, StageOrder) {
		t.Fatalf("all: %v %v", got, err)
	}
	for _, path := range []string{"../secret", "/etc/passwd", "build/../../secret", ".git/config", "manifest.json", "a\\b", "."} {
		if ValidateArtifacts([]string{path}) == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if err := ValidateArtifacts([]string{"build/arm64/diag", "app.ipk"}); err != nil {
		t.Fatal(err)
	}
	if ValidateArtifacts([]string{"app.ipk", "app.ipk"}) == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestPipelineProfileDigestInvalidatesAllConfig(t *testing.T) {
	original := diagProfile()
	digest := ProfileDigest(original)
	mutations := []func(*Profile){
		func(p *Profile) {
			w := p.Workflows["build"]
			w.Args = append(w.Args, "-race")
			p.Workflows["build"] = w
		},
		func(p *Profile) {
			w := p.Workflows["build"]
			w.Env = map[string]string{"GOARCH": "amd64"}
			p.Workflows["build"] = w
		},
		func(p *Profile) { w := p.Workflows["build"]; w.Artifacts = []string{"other"}; p.Workflows["build"] = w },
		func(p *Profile) { w := p.Workflows["test"]; w.TimeoutSeconds++; p.Workflows["test"] = w },
	}
	for _, mutate := range mutations {
		p := cloneProfile(original)
		mutate(&p)
		if ProfileDigest(p) == digest {
			t.Fatal("config did not invalidate evidence")
		}
	}
}
