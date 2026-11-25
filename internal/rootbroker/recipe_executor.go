package rootbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"mcpshka/internal/budget"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/recipecontract"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/runtimeunitbind"
)

var recipeRepository = regexp.MustCompile(`^/root/repos/[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// validateRecipePrepare accepts fixed make-test or registered whole-tree argv.
func validateRecipePrepare(args []string) error {
	if len(args) < 5 || args[0] != "prepare-recipe" || !validHex(args[1], 32) || !recipeRepository.MatchString(args[2]) || filepath.Clean(args[2]) != args[2] || !validHex(args[3], 40) {
		return errors.New("recipe preparation requires fixed bootstrap, immediate repository and commit identities")
	}
	if _, err := recipePrepareTimeout(args[4]); err != nil {
		return err
	}
	if len(args) > 5 {
		extras, argv, err := recipeTail(args)
		if err != nil || !executor.ValidTreeCommand(argv) || len(extras.Paths) > 8 {
			return errors.New("registered test argv is outside the sealed command boundary")
		}
	}
	return nil
}

type RecipeExtras = recipecontract.RecipeExtras

func EncodeRecipeExtras(paths []string, env map[string]string) string {
	return recipecontract.EncodeRecipeExtras(paths, env)
}

func recipeTail(args []string) (RecipeExtras, []string, error) {
	var extras RecipeExtras
	tail := args[5:]
	if len(tail) > 0 && strings.HasPrefix(tail[0], "spec=") {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tail[0], "spec="))
		if err != nil || len(raw) > 8192 {
			return extras, nil, errors.New("invalid recipe extras")
		}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if d.Decode(&extras) != nil || (len(extras.Paths) == 0 && len(extras.Env) == 0 && extras.Stage == "") || (extras.Stage != "" && !projectprofile.ValidStage(extras.Stage)) || projectprofile.ValidateArtifacts(extras.Artifacts) != nil || (len(extras.Artifacts) > 0 && extras.Stage != "package") {
			return extras, nil, errors.New("invalid recipe extras")
		}
		tail = tail[1:]
	}
	return extras, append([]string(nil), tail...), nil
}

// recipeSource names the sealed snapshot kind and argv implied by validated args.
func recipeSource(args []string) (string, []string) {
	if len(args) == 5 {
		return "make:test", []string{"make", "test"}
	}
	_, argv, _ := recipeTail(args)
	return executor.TreeTestPackage, argv
}

func recipePrepareTimeout(raw string) (int64, error) {
	timeout, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || timeout < 1 || timeout > budget.CeilingSeconds || strconv.FormatInt(timeout, 10) != raw {
		return 0, executor.Hold(failure.PolicyDenied, "RECIPE_TIMEOUT_INVALID", "rootbroker.recipePrepareTimeout", "recipe timeout must be canonical and within 1 s..the flexible-budget ceiling (6 h)", err)
	}
	return timeout, nil
}

func RecipePrepareIdentity(bootstrapID string) (executor.Pin, string, error) {
	return recipecontract.RecipePrepareIdentity(bootstrapID)
}

func recipePrepareServiceArgs(args []string) (string, []string, error) {
	if err := validateRecipePrepare(args); err != nil {
		return "", nil, err
	}
	unit := recipecontract.RecipePrepareUnit(args)
	// Parent bind preserves child symlink evidence; constructor never executes the recipe.
	// Stderr carries refusal; stdout is unused (MCP-146).
	command := []string{"--unit=" + unit, "--no-block", "--quiet",
		"--property=StandardOutput=null", "--property=StandardError=journal", "--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		// Preparation has its own bounded lifetime, outside the broker RPC.
		"--property=RuntimeMaxSec=" + strconv.Itoa(int(budget.PhaseMax(RecipePrepareDefault)/time.Second)+5) + "s", "--property=TimeoutStopSec=5s", "--property=MemoryMax=256M", "--property=CPUQuota=100%", "--property=TasksMax=16", "--property=KillMode=control-group", "--property=LimitFSIZE=134217728",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes", "--property=PrivateNetwork=yes",
		// Optional read-only module cache, just like the module gate.
		"--property=ProtectSystem=strict", "--property=TemporaryFileSystem=/root:mode=0711 /home /run/user", "--property=BindReadOnlyPaths=/root/repos -" + executor.ModuleCacheRoot, "--property=ReadWritePaths=" + executor.AuthorityRoot,
		// The sealed workspace is handed to the service UID before sealing.
		"--property=CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER",
		// Git probes need service UID; seccomp requires ambient caps.
		"--property=AmbientCapabilities=CAP_SETUID CAP_SETGID",
		"--property=RestrictAddressFamilies=AF_UNIX", "--property=RestrictNamespaces=yes",
		"--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		executorHelper, "recipe-prepare-worker"}
	return unit, heavyqueue.HeavyUnitArgs(append(command, args...)), nil
}

func preparedRecipeManifest(args []string) (executor.Manifest, error) {
	p, _, err := RecipePrepareIdentity(args[1])
	if err != nil {
		return executor.Manifest{}, err
	}
	_, m, err := executor.OpenPermit(p.PermitID)
	if err != nil {
		return executor.Manifest{}, err
	}
	if !recipePreparedManifestMatches(m, args) {
		return executor.Manifest{}, executor.Hold(failure.FencedUnknown, "RECIPE_MANIFEST_MISMATCH", "rootbroker.preparedRecipeManifest", "sealed recipe manifest does not match preparation", nil)
	}
	return m, nil
}

func recipePreparedManifestMatches(m executor.Manifest, args []string) bool {
	if validateRecipePrepare(args) != nil {
		return false
	}
	p, launch, _ := RecipePrepareIdentity(args[1])
	timeout, _ := recipePrepareTimeout(args[4])
	pkg, argv := recipeSource(args)
	spec, ok := m.Runs[launch]
	if m.Pin != p || m.Purpose != "project-recipe-test-v1" || m.Source == nil || m.Source.Repo != args[2] || m.Source.Commit != args[3] || m.Source.Package != pkg || len(m.Runs) != 1 || !ok || spec.Profile != "recipe-test-v1" || !slices.Equal(spec.Command, argv) || spec.TimeoutSeconds != timeout {
		return false
	}
	extras := RecipeExtras{}
	if len(args) > 5 {
		extras, _, _ = recipeTail(args)
	}
	if spec.Stage != extras.Stage || !slices.Equal(spec.Artifacts, extras.Artifacts) || len(spec.Runtime) != len(extras.Paths) || !maps.Equal(spec.Env, extras.Env) {
		return false
	}
	for i, b := range spec.Runtime {
		if b.Path != extras.Paths[i] {
			return false
		}
	}
	return true
}

func runRecipePrepare(ctx context.Context, args []string) (result []byte, retErr error) {
	budget := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			retErr = failure.Context(ctx, "rootbroker.runRecipePrepare", budget, retErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	unit, command, err := recipePrepareServiceArgs(args)
	if err != nil {
		return nil, err
	}
	if m, err := preparedRecipeManifest(args); err == nil {
		return json.Marshal(m)
	}
	if state, found, err := recipePreparationState(ctx, args, unit); err != nil {
		return nil, err
	} else if found {
		return json.Marshal(state)
	}
	if err := validateRootOwnedExecutable(executorHelper); err != nil {
		return nil, err
	}
	// MCP-197: the offline sandbox copies modules only from the trusted cache.
	warmGoModules(ctx, args[2], args[3])
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil || stderr.truncated {
		// A lost scheduling reply is unresolved; never start another unit.
		return nil, executor.Hold(failure.FencedUnknown, "RECIPE_SCHEDULE_UNVERIFIED", "rootbroker.runRecipePrepare", "recipe scheduling unresolved; observe recipe-status before retry", err)
	}
	p, launch, _ := RecipePrepareIdentity(args[1])
	return json.Marshal(RecipePrepareState{State: "PREPARING", PermitID: p.PermitID, LaunchID: launch})
}

type RecipePrepareState = recipecontract.RecipePrepareState

func recipePreparationState(ctx context.Context, args []string, unit string) (RecipePrepareState, bool, error) {
	p, launch, _ := RecipePrepareIdentity(args[1])
	out := RecipePrepareState{State: "PREPARING", PermitID: p.PermitID, LaunchID: launch}
	if _, err := preparedRecipeManifest(args); err == nil {
		out.State = "SEALED"
		return out, true, nil
	}
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus", unit+".service")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	raw := &limitedBuffer{limit: 4096}
	cmd.Stdout = raw
	err := cmd.Run()
	if child.Err() != nil || raw.truncated {
		return out, false, executor.Hold(failure.FencedUnknown, "RECIPE_STATUS_UNVERIFIED", "rootbroker.recipePreparationState", "recipe preparation status unavailable", err)
	}
	state, found, parseErr := prepareUnitState(modulePrepareState{State: "PREPARING"}, "1", raw.Bytes())
	if parseErr != nil || (err != nil && found) {
		return out, false, executor.Hold(failure.FencedUnknown, "RECIPE_STATUS_UNVERIFIED", "rootbroker.recipePreparationState", "recipe preparation status unavailable", errors.Join(err, parseErr))
	}
	if !found || state.State == "FAILED" {
		// A successful transient unit is briefly inactive, then collected,
		// right after sealing; only the manifest distinguishes it.
		if _, err := preparedRecipeManifest(args); err == nil {
			out.State = "SEALED"
			return out, true, nil
		}
	}
	out.State = state.State
	return out, found, nil
}

// runRecipeStatus observes the derived unit without scheduling or launching.
func runRecipeStatus(ctx context.Context, args []string) ([]byte, error) {
	prepare := append([]string{"prepare-recipe"}, args[1:]...)
	unit, _, err := recipePrepareServiceArgs(prepare)
	if err != nil {
		return nil, err
	}
	state, found, err := recipePreparationState(ctx, prepare, unit)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, executor.Hold(failure.FencedUnknown, "RECIPE_PREPARATION_ABSENT", "rootbroker.runRecipeStatus", "recipe preparation not found; reconcile before retry", nil)
	}
	return json.Marshal(state)
}

// runRecipeAbandon settles a preparation that failed before sealing a permit,
// so its job can terminate instead of holding the project forever.
func runRecipeAbandon(ctx context.Context, args []string) (result []byte, retErr error) {
	budget := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			retErr = failure.Context(ctx, "rootbroker.runRecipeAbandon", budget, retErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prepare := append([]string{"prepare-recipe"}, args[1:]...)
	if err := recipecontract.ValidateRecipeRequest(args, validateRecipePrepare); err != nil {
		return nil, err
	}
	unit := recipecontract.RecipePrepareUnit(prepare)
	if _, err := preparedRecipeManifest(prepare); err == nil {
		return nil, errors.New("recipe permit exists; observe executor status: HOLD")
	}
	alive, err := administrativeWorkerAlive(ctx, unit+".service")
	if err != nil {
		return nil, executor.Hold(failure.FencedUnknown, "RECIPE_WORKER_UNVERIFIED", "rootbroker.runRecipeAbandon", "recipe worker liveness unverified", err)
	}
	if alive {
		return nil, executor.Hold(failure.OwnerBusy, "RECIPE_WORKER_UNRESOLVED", "rootbroker.runRecipeAbandon", "recipe worker may still be alive", err)
	}
	timeout, _ := recipePrepareTimeout(args[4])
	pkg, _ := recipeSource(prepare)
	if err := executor.AbandonRecipePreparation(args[1], args[2], args[3], pkg, timeout); err != nil {
		return nil, err
	}
	// The failed constructor unit was the evidence for this proof; clear it so
	// it does not linger in systemctl --failed (BUG-20261021-31).
	reset, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(reset, "/usr/bin/systemctl", "reset-failed", unit+".service").Run()
	p, _, _ := RecipePrepareIdentity(args[1])
	return json.Marshal(map[string]string{"state": "ABANDONED", "permit_id": p.PermitID})
}

func recipeSourceClean(ctx context.Context, repo, commit string) (retErr error) {
	budget := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			retErr = failure.Context(ctx, "rootbroker.recipeSourceClean", budget, retErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := projectlease.ValidateGitStatusInputs(ctx, repo); err != nil {
		return err
	}
	account, err := user.Lookup("mcpshka")
	if err != nil || account.Uid != "994" {
		return executor.Hold(failure.PolicyDenied, "RECIPE_ACCOUNT_INVALID", "rootbroker.recipeSourceClean", "recipe Git account unavailable or unexpected", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || gid == 0 || gid == uint64(^uint32(0)) {
		return executor.Hold(failure.PolicyDenied, "RECIPE_GROUP_INVALID", "rootbroker.recipeSourceClean", "recipe Git group invalid", err)
	}
	git := func(args ...string) (string, error) {
		fixed := []string{"--no-replace-objects", "--no-lazy-fetch", "-c", "safe.directory=" + repo, "-c", "protocol.allow=never", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", repo}
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 994, Gid: uint32(gid), NoSetGroups: false}}
		out := &limitedBuffer{limit: 8192}
		cmd.Stdout = out
		if err := cmd.Run(); err != nil || out.truncated {
			return "", executor.Hold(failure.FailedVerified, "RECIPE_GIT_FAILED", "rootbroker.recipeSourceClean.git", "recipe Git read failed or exceeded output limit", err)
		}
		return string(out.Bytes()), nil
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) != repo {
		return executor.Hold(failure.FencedUnknown, "RECIPE_ROOT_MISMATCH", "rootbroker.recipeSourceClean", "recipe repository root unverified", err)
	}
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(head) != commit {
		return executor.Hold(failure.FencedUnknown, "RECIPE_HEAD_MISMATCH", "rootbroker.recipeSourceClean", "recipe commit changed or unverified", err)
	}
	dirty, err := git("status", "--porcelain=v1", "--untracked-files=no", "--ignore-submodules=all")
	if err != nil || dirty != "" {
		return executor.Hold(failure.FencedUnknown, "RECIPE_SOURCE_DIRTY", "rootbroker.recipeSourceClean", "recipe source is dirty or unverified", err)
	}
	return nil
}

// RunRecipePrepareWorker is dispatched only by the installed fixed PID1 helper.
// Missing authority storage remains HOLD; initialization belongs to installation.
func RunRecipePrepareWorker(args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, executor.Hold(failure.PolicyDenied, "RECIPE_ROOT_REQUIRED", "rootbroker.RunRecipePrepareWorker", "recipe worker requires root", nil)
	}
	if err := validateRecipePrepare(args); err != nil {
		return nil, err
	}
	for _, path := range []string{"/root/repos", args[2]} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, executor.Hold(failure.PolicyDenied, "RECIPE_PATH_INVALID", "rootbroker.RunRecipePrepareWorker", "recipe scope must be an existing directory without aliases", err)
		}
		if path == "/root/repos" {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return nil, executor.Hold(failure.PolicyDenied, "RECIPE_PARENT_UNTRUSTED", "rootbroker.RunRecipePrepareWorker", "recipe parent ownership or permissions invalid", nil)
			}
		}
	}
	resolved, err := filepath.EvalSymlinks(args[2])
	if err != nil || resolved != args[2] {
		return nil, executor.Hold(failure.PolicyDenied, "RECIPE_PATH_ALIASED", "rootbroker.RunRecipePrepareWorker", "recipe repository path is aliased or unavailable", err)
	}
	limit := budget.Phase(RecipePrepareDefault)
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	m, err := prepareRecipeWorker(ctx, args)
	if err != nil && ctx.Err() != nil {
		// Executor checks answer a bare HOLD; name the expired budget so the
		// journal says why (08.11: 91 s under load 5 read as "fencing").
		return nil, failure.Context(ctx, "rootbroker.RunRecipePrepareWorker", limit, err)
	}
	return m, err
}

const RecipePrepareDefault = recipecontract.RecipePrepareDefault

func prepareRecipeWorker(ctx context.Context, args []string) ([]byte, error) {
	if err := recipeSourceClean(ctx, args[2], args[3]); err != nil {
		return nil, err
	}
	timeout, _ := recipePrepareTimeout(args[4])
	prepare := func() (executor.Manifest, error) {
		return executor.PrepareRecipeTest(ctx, args[1], args[2], args[3], timeout)
	}
	if pkg, argv := recipeSource(args); pkg == executor.TreeTestPackage {
		extras, _, _ := recipeTail(args)
		prepare = func() (executor.Manifest, error) {
			return executor.PrepareTreeTest(ctx, args[1], args[2], args[3], timeout, argv, extras.Paths, extras.Env, executor.StageSpec{Stage: extras.Stage, Artifacts: extras.Artifacts})
		}
	}
	m, err := prepare()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func recipeTestUnitArgs(permitID, launchID, pkg string, spec executor.RunSpec) ([]string, error) {
	if !executorID.MatchString(permitID) || !executorID.MatchString(launchID) || spec.Profile != "recipe-test-v1" || !executor.RecipeCommandMatches(pkg, spec.Command) || spec.TimeoutSeconds < 1 || spec.TimeoutSeconds > budget.CeilingSeconds {
		return nil, executor.Hold(failure.PolicyDenied, "RECIPE_TEST_SPEC_INVALID", "rootbroker.recipeTestUnitArgs", "recipe launch identity command profile or timeout invalid", nil)
	}
	// Module limits with recipe-specific timeout.
	args := moduleTestUnitArgs(permitID, launchID)
	for i, a := range args {
		if a == "--property=RuntimeMaxSec=25200s" {
			args[i] = "--property=RuntimeMaxSec=" + strconv.FormatInt(spec.TimeoutSeconds+21600, 10) + "s"
		}
		// Only dependency setup needs network.
		if a == "--property=PrivateNetwork=yes" && spec.EnvBuild != nil {
			args[i] = "--property=PrivateNetwork=no"
		}
		// Publishing dependency build data requires CAP_CHOWN.
		if a == "--property=CapabilityBoundingSet=CAP_SETUID CAP_SETGID CAP_DAC_OVERRIDE CAP_KILL" && spec.EnvBuild != nil {
			args[i] = "--property=CapabilityBoundingSet=CAP_SETUID CAP_SETGID CAP_DAC_OVERRIDE CAP_KILL CAP_CHOWN CAP_FOWNER"
		}
	}
	key := ""
	if spec.EnvBuild != nil {
		key = spec.EnvBuild.Key
	}
	var err error
	args, err = runtimeunitbind.RecipeArgs(args, key, spec.Runtime)
	if err != nil {
		return nil, executor.Hold(failure.PolicyDenied, "RECIPE_RUNTIME_TARGET_INVALID", "rootbroker.recipeTestUnitArgs", "sealed runtime target has an unsafe path", err)
	}
	return args, nil
}
