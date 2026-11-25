package rootbroker

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestPipelineBrokerBoundedArtifactReads(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{{"pipeline-receipt", id}, {"pipeline-read", id, "0", "0"}, {"pipeline-read", id, "15", "67108864"}} {
		if err := validateExecutor(args); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, args := range [][]string{{"pipeline-receipt", "../secret"}, {"pipeline-read", id, "16", "0"}, {"pipeline-read", id, "0", "-1"}, {"pipeline-read", id, "0", "67108865"}, {"pipeline-read", id, "00", "0"}} {
		if validateExecutor(args) == nil {
			t.Fatalf("unsafe read: %v", args)
		}
	}
}

func TestPipelineRecipeExtrasValidation(t *testing.T) {
	for _, extra := range []RecipeExtras{{Stage: "package", Artifacts: []string{"app.ipk"}}, {Stage: "verify"}} {
		raw, _ := json.Marshal(extra)
		args := []string{"prepare-recipe", strings.Repeat("a", 32), "/root/repos/fixture", strings.Repeat("b", 40), "60", "spec=" + base64.RawURLEncoding.EncodeToString(raw), "make", "verify"}
		if err := validateRecipePrepare(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, extra := range []RecipeExtras{{Stage: "deploy"}, {Stage: "verify", Artifacts: []string{"app.ipk"}}, {Stage: "package", Artifacts: []string{"../secret"}}} {
		raw, _ := json.Marshal(extra)
		args := []string{"prepare-recipe", strings.Repeat("a", 32), "/root/repos/fixture", strings.Repeat("b", 40), "60", "spec=" + base64.RawURLEncoding.EncodeToString(raw), "make", "verify"}
		if validateRecipePrepare(args) == nil {
			t.Fatalf("unsafe extras: %+v", extra)
		}
	}
}
