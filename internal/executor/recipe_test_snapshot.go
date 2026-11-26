package executor

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/budget"
	"mcpshka/internal/failure"
	"mcpshka/internal/repopath"
	"mcpshka/internal/sandboxbind"
	"mcpshka/internal/snapshotfiles"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const recipeTestPackage = "make:test"

// The executor and broker must use the same flexible timeout ceiling.
// Otherwise a history-raised budget can fail before sealing a permit and
// leave an UNKNOWN job whose abandon path rejects the same budget.
func validRecipeTimeout(seconds int64) bool {
	return seconds >= 1 && seconds <= budget.CeilingSeconds
}

// treeTestPackage seals every tracked file of a commit and runs the project's
// own registered test argv; the sandbox, not the argv, is the boundary.
const treeTestPackage = "tree:all"

// TreeTestPackage names the whole-tree source snapshot for observers.
const TreeTestPackage = treeTestPackage

func ValidTreeCommand(argv []string) bool { return snapshotfiles.ValidTreeCommand(argv) }

// RecipeCommandMatches pins the legacy Make snapshot to `make test`; a whole
// tree carries its registered argv.
func RecipeCommandMatches(pkg string, argv []string) bool {
	if pkg == recipeTestPackage {
		return len(argv) == 2 && argv[0] == "make" && argv[1] == "test"
	}
	return pkg == treeTestPackage && ValidTreeCommand(argv)
}

func treeTestPath(name string) bool { return snapshotfiles.TreePath(name) }

// PrepareTreeTest seals all tracked files of a commit with a registered argv.
func PrepareTreeTest(ctx context.Context, bootstrapID, repo, commit string, timeout int64, argv []string, runtimePaths []string, env map[string]string, stages ...StageSpec) (Manifest, error) {
	if len(stages) > 1 || !validRecipeTimeout(timeout) || !ValidTreeCommand(argv) || !sandboxbind.ValidEnv(env) {
		return Manifest{}, Hold(failure.PolicyDenied, "TREE_TEST_SPEC_INVALID", "executor.PrepareTreeTest", "test command, timeout, stage or env (A-Z keys, no PATH/HOME/LD_*) invalid", nil)
	}
	binds, err := sandboxbind.Resolve(runtimePaths, RuntimeAllowlist, repo)
	if err != nil {
		return Manifest{}, err
	}
	project, err := detectEnv(ctx, repo, commit)
	if err != nil {
		return Manifest{}, err
	}
	if project != nil {
		// The worker unit binds this directory; it must exist before launch.
		if _, ready := envReadyDir(project.Key); !ready {
			if _, err := prepareEnvBuild(project.Key); err != nil {
				return Manifest{}, err
			}
		}
	}
	m, err := prepareSnapshotTest(ctx, bootstrapID, repo, commit, treeTestPackage, "project-recipe-test-v1", "recipe-test-v1", append([]string(nil), argv...), timeout, func(spec *RunSpec) {
		spec.Runtime, spec.Env, spec.EnvBuild = binds, env, project
		if len(stages) == 1 {
			spec.Stage = stages[0].Stage
			spec.Artifacts = stages[0].Artifacts
		}
	})
	return m, err
}

// PrepareRecipeTest seals committed Make test inputs, never a host Make command.
// The returned permit still needs bounded broker launch and verified fencing.
func PrepareRecipeTest(ctx context.Context, bootstrapID, repo, commit string, timeoutSeconds ...int64) (Manifest, error) {
	timeout := int64(1800)
	if len(timeoutSeconds) > 1 {
		return Manifest{}, ErrHold
	}
	if len(timeoutSeconds) == 1 {
		timeout = timeoutSeconds[0]
	}
	if !validRecipeTimeout(timeout) {
		return Manifest{}, ErrHold
	}
	return prepareSnapshotTest(ctx, bootstrapID, repo, commit, recipeTestPackage, "project-recipe-test-v1", "recipe-test-v1", []string{"make", "test"}, timeout)
}

// AbandonRecipePreparation proves a recipe bootstrap can never yield a permit:
// its manifest is absent and its one-use claim is held, claimed here as a
// tombstone when the constructor never reached it. The caller must first prove
// that no constructor unit for this bootstrap is still running.
func AbandonRecipePreparation(bootstrapID, repo, commit, pkg string, timeoutSeconds int64) error {
	if os.Geteuid() != 0 || !idPattern.MatchString(bootstrapID) || !commitPattern.MatchString(commit) || filepath.Clean(repo) != repo || !strings.HasPrefix(repo, "/root/repos/") || !validRecipeTimeout(timeoutSeconds) || (pkg != recipeTestPackage && pkg != treeTestPackage) {
		return ErrHold
	}
	p, _ := snapshotIdentity("project-recipe-test-v1", bootstrapID)
	manifest := filepath.Join(AuthorityRoot, p.PermitID+".manifest.json")
	if _, e := os.Lstat(manifest); !errors.Is(e, os.ErrNotExist) {
		return ErrHold
	}
	c, e := OpenAuthority()
	if e != nil {
		return e
	}
	if repopath.IsManagedWorktreePath(repo) {
		// Legacy paths could not enter preparation. Any existing claim means
		// the scope needs reconciliation. Leave it and the authority untouched.
		_, err := c.db.Get("executor/bootstrap/" + bootstrapID + ".json")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ErrHold
	}
	return abandonRecipeClaim(c, bootstrapID, map[string]string{"permit_id": p.PermitID, "repo": repo, "commit": commit, "package": pkg, "timeout_seconds": strconv.FormatInt(timeoutSeconds, 10)}, manifest)
}

func abandonRecipeClaim(c *Controller, bootstrapID string, fields map[string]string, manifest string) error {
	key := "executor/bootstrap/" + bootstrapID + ".json"
	tombstone := map[string]string{"abandoned": "true"}
	for k, v := range fields {
		tombstone[k] = v
	}
	raw, _ := json.Marshal(tombstone)
	ok, e := c.db.PutIfAbsent(key, raw)
	if e != nil || ok {
		return e
	}
	if raw, e = c.db.Get(key); e != nil {
		return e
	}
	var claim map[string]string
	if json.Unmarshal(raw, &claim) != nil {
		return ErrHold
	}
	delete(claim, "abandoned")
	if len(claim) != len(fields) {
		return ErrHold
	}
	for k, v := range fields {
		if claim[k] != v {
			return ErrHold
		}
	}
	// A claimed constructor that sealed its manifest meanwhile owns a permit.
	if _, e := os.Lstat(manifest); !errors.Is(e, os.ErrNotExist) {
		return ErrHold
	}
	return nil
}

func recipeTestPath(name string) bool { return snapshotfiles.RecipePath(name) }
