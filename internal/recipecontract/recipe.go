// Package recipecontract holds the wire identities shared by broker and pipeline runtime.
package recipecontract

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"mcpshka/internal/repopath"
	"regexp"
	"time"
)

// RecipeExtras are a registered suite's runtime paths and environment, sent
// as one "spec=<base64url JSON>" argument ahead of the argv.
type RecipeExtras struct {
	Stage     string            `json:"stage,omitempty"`
	Artifacts []string          `json:"artifacts,omitempty"`
	Paths     []string          `json:"paths,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// EncodeRecipeExtras returns the broker argument for extras, or "" if empty.
func EncodeRecipeExtras(paths []string, env map[string]string) string {
	if len(paths) == 0 && len(env) == 0 {
		return ""
	}
	raw, _ := json.Marshal(RecipeExtras{Paths: paths, Env: env})
	return "spec=" + base64.RawURLEncoding.EncodeToString(raw)
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// RecipePrepareIdentity lets observers reconcile a lost preparation response
// through existing executor status, without replaying preparation or launch.
func RecipePrepareIdentity(bootstrapID string) (executor.Pin, string, error) {
	if !idPattern.MatchString(bootstrapID) {
		return executor.Pin{}, "", executor.Hold(failure.PolicyDenied, "RECIPE_BOOTSTRAP_INVALID", "rootbroker.RecipePrepareIdentity", "recipe bootstrap identity invalid", nil)
	}
	derive := func(label string) string {
		sum := sha256.Sum256([]byte("project-recipe-test-v1:" + bootstrapID + ":" + label))
		return hex.EncodeToString(sum[:16])
	}
	return executor.Pin{PermitID: derive("permit"), LeaseID: derive("lease"), ExecutionID: derive("execution"), Generation: 1}, derive("launch"), nil
}

// RecipePrepareUnit identifies the scheduled constructor for these exact arguments.
func RecipePrepareUnit(prepare []string) string {
	raw, _ := json.Marshal(prepare)
	sum := sha256.Sum256(raw)
	return "mcpshka-recipe-prepare-" + hex.EncodeToString(sum[:16])
}

// ValidateRecipeRequest accepts the old linked path only to reconcile a
// refusal. It preserves the original request for the unit and permit probes.
func ValidateRecipeRequest(args []string, validate func([]string) error) error {
	if len(args) == 0 {
		return validate(nil)
	}
	prepare := append([]string{"prepare-recipe"}, args[1:]...)
	if args[0] == "abandon-recipe" && len(args) > 2 && repopath.IsManagedWorktreePath(args[2]) {
		prepare[2] = "/root/repos/wt"
	}
	return validate(prepare)
}

// RecipePrepareState describes scheduling only, never authority to launch.
type RecipePrepareState struct {
	State    string `json:"state"`
	PermitID string `json:"permit_id"`
	LaunchID string `json:"launch_id"`
}

// RecipePrepareDefault is snapshot preparation's flexible budget at idle;
// load stretches it up to budget.PhaseMax. It was a fixed 90 s and expired on
// a loaded H96 (load 5, CI publish alongside) before sealing.
const RecipePrepareDefault = 2 * time.Minute

// Whole-tree snapshots may materialize thousands of blobs and copy the
// committed Go module set on the low-priority disk queue. MCP-270:
// go2rtc's constructor exhausted 120s with only 22 CPU-seconds used.
// Give whole-tree preparation its own bounded budget, shared by PID1,
// the worker and the observer; do not extend the legacy Make path.
const RecipeTreePrepareDefault = 5 * time.Minute

func RecipePrepareBudget(wholeTree bool) time.Duration {
	if wholeTree {
		return RecipeTreePrepareDefault
	}
	return RecipePrepareDefault
}
