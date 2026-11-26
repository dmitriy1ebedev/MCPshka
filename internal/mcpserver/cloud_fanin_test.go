package mcpserver

import (
	"strings"
	"testing"
)

func cloudFixture() CloudFanInInput {
	s := strings.Repeat("a", 64)
	return CloudFanInInput{
		Plan: CloudPlan{Schema: cloudPlanSchema, ReleaseID: "release1", SourceDigest: s,
			Required: map[string][]string{"github": {"release", "race"}, "gitverse": {"arm64"}, "moshub": {"sast"}}},
		Observations: []CloudObservation{
			{Schema: cloudObsSchema, ReleaseID: "release1", SourceDigest: s, Provider: "github", Suite: "release", RunID: "1", Status: "success"},
			{Schema: cloudObsSchema, ReleaseID: "release1", SourceDigest: s, Provider: "github", Suite: "race", RunID: "2", Status: "success"},
			{Schema: cloudObsSchema, ReleaseID: "release1", SourceDigest: s, Provider: "gitverse", Suite: "arm64", RunID: "3", Status: "success"},
			{Schema: cloudObsSchema, ReleaseID: "release1", SourceDigest: s, Provider: "moshub", Suite: "sast", RunID: "4", Status: "success"},
		},
	}
}

func TestCloudFanInObservedNeverDeploys(t *testing.T) {
	res, err := cloudFanIn(cloudFixture())
	if err != nil || res.State != "observed_pass" || res.DeployAuthorized || res.Trust != "UNTRUSTED" || len(res.Runs) != 4 {
		t.Fatalf("observations must not grant deploy: %#v %v", res, err)
	}
}

func TestCloudFanInRejectsForgedEvidence(t *testing.T) {
	for _, name := range []string{"mismatch", "duplicate", "authority", "unexpected", "bad_status", "bad_plan", "long_run"} {
		in := cloudFixture()
		switch name {
		case "mismatch":
			in.Observations[0].SourceDigest = strings.Repeat("b", 64)
		case "duplicate":
			in.Observations = append(in.Observations, in.Observations[0])
		case "authority":
			in.Observations[0].DeployAuthorized = true
		case "unexpected":
			in.Observations[0].Suite = "other"
		case "bad_status":
			in.Observations[0].Status = "pass"
		case "bad_plan":
			in.Plan.Required["github"] = []string{"release", "release"}
		case "long_run":
			in.Observations[0].RunID = strings.Repeat("Z", 200)
		}
		if _, err := cloudFanIn(in); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}

func TestCloudFanInMissingFailureHold(t *testing.T) {
	in := cloudFixture()
	in.Observations = in.Observations[:3]
	if out, err := cloudFanIn(in); err != nil || out.State != "hold" || len(out.Missing) != 1 {
		t.Fatalf("missing suite must HOLD: %#v %v", out, err)
	}
	in = cloudFixture()
	in.Observations[1].Status = "failure"
	if out, err := cloudFanIn(in); err != nil || out.State != "failed" || len(out.Failed) != 1 {
		t.Fatalf("failure should block: %#v %v", out, err)
	}
}
