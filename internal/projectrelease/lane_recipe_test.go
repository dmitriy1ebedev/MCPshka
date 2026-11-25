package projectrelease

import (
	"os"
	"regexp"
	"testing"
)

// MCP-171: Lane's real recipe (a copy of lane/.mcpshka/release.json 2.0.4)
// parses, and its expressions match the shapes Lane prints: the top-level
// status of gate-status and upgrade run-status, not a nested one.
func TestLaneRecipeMatchesLaneOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/lane_release.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRecipe(raw)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]Step{}
	for _, s := range r.Steps {
		steps[s.Name] = s
	}
	re := func(s string) *regexp.Regexp { return regexp.MustCompile(s) }
	stage := "{\n  \"ok\": true,\n  \"release_id\": \"release-0e5a10183d6b-cf960dab8a80\"\n}"
	if m := re(steps["stage"].Capture["release_id"]).FindStringSubmatch(stage); m == nil || m[1] != "release-0e5a10183d6b-cf960dab8a80" {
		t.Fatalf("stage capture: %v", m)
	}
	gateRunning := "{\n  \"ok\": true,\n  \"status\": \"RUNNING\",\n  \"result\": {\n    \"status\": \"ERROR\"\n  }\n}"
	if re(steps["gate"].Expect).MatchString(gateRunning) || re(steps["gate"].Fail).MatchString(gateRunning) {
		t.Fatal("a nested status decided the gate")
	}
	if !re(steps["gate"].Expect).MatchString("{\n  \"ok\": true,\n  \"status\": \"SUCCESS\"\n}") {
		t.Fatal("gate SUCCESS not recognised")
	}
	awaiting := "{\n  \"ok\": true,\n  \"run_id\": \"upgrade-20261124T164140Z-d512bfca\",\n  \"status\": \"AWAITING_CONFIRM\"\n}"
	if !re(steps["await"].Expect).MatchString(awaiting) || re(steps["await"].Fail).MatchString(awaiting) {
		t.Fatal("AWAITING_CONFIRM must pass the wait")
	}
	if m := re(steps["upgrade"].Capture["upgrade_run"]).FindStringSubmatch(awaiting); m == nil || m[1] != "upgrade-20261124T164140Z-d512bfca" {
		t.Fatalf("upgrade run capture: %v", m)
	}
	if !re(steps["await"].Fail).MatchString("{\n  \"status\": \"ROLLED_BACK\"\n}") {
		t.Fatal("a rolled back upgrade must fail the wait")
	}
	if !re(steps["route"].Expect).MatchString("default dev lanetun0 scope link") {
		t.Fatal("table 110 via lanetun0 not recognised")
	}
	if steps["confirm"].Name == "" || r.Steps[len(r.Steps)-1].Name != "confirm" || r.VersionFile != "VERSION" {
		t.Fatal("confirm must be the last step, after the checks; VERSION declares the version")
	}
}
