// Package workbenchruntime binds ordinary jobs to the sealed root executor.
package workbenchruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/recipecontract"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/budget"
	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/repopath"
)

const StageCommand = "mcpshka-project-stage"
const WorkflowCommand = "mcpshka-project-workflow"

const runtimeName = "mcpshka-workbench/v1"

type Runtime struct {
	policy  *policy.Policy
	call    func(context.Context, string, ...string) ([]byte, error)
	now     func() time.Time
	resolve func(string) (projectprofile.Resolution, error)
	// cpuUsage reads a unit's CPU time (nil: systemctl show); progress
	// remembers the last advance per launch for the budget extension.
	cpuUsage func(unit string) (uint64, error)
	progress sync.Map // launch → *cpuSample
}

type cpuSample struct {
	mu       sync.Mutex
	nsec     uint64
	advanced time.Time
}
type binding struct {
	Schema         string       `json:"schema"`
	JobID          string       `json:"job_id"`
	Root           string       `json:"root"`
	Commit         string       `json:"commit"`
	Bootstrap      string       `json:"bootstrap"`
	Pin            executor.Pin `json:"pin"`
	Launch         string       `json:"launch"`
	TimeoutSeconds int64        `json:"timeout_seconds"`
	StartedAt      time.Time    `json:"started_at"`
	// Argv is the registered test command of a whole-tree snapshot; empty
	// means the legacy DIAG `make test` snapshot.
	Argv      []string `json:"argv,omitempty"`
	Stage     string   `json:"stage,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	// Paths and Env are the profile's runtime dependencies (tree only).
	Paths []string          `json:"paths,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	// Attempt counts automatic re-preparations after a proven no-effect
	// preparation failure; each gets a fresh StartedAt, hence identity.
	Attempt int `json:"attempt,omitempty"`
}

// maxPrepareAttempts bounds automatic re-preparation of one job (owner,
// 08.11: «работа должна продолжаться в авто режиме»).
const maxPrepareAttempts = 3

// New resolves project profiles through profiles (registered, repository or
// builtin); nil falls back to repository and builtin profiles only.
func New(p *policy.Policy, profiles ...*projectprofile.Service) *Runtime {
	service := projectprofile.New(p)
	if len(profiles) == 1 && profiles[0] != nil {
		service = profiles[0]
	}
	return &Runtime{policy: p, call: brokerclient.Run, now: time.Now, resolve: service.Resolve}
}
func (r *Runtime) Name() string { return runtimeName }
func (r *Runtime) Match(command string, args []string, root string) bool {
	_, ok := r.sealedArgv(command, args, root)
	return ok
}

// sealedExtras returns the resolved test workflow's runtime paths and env.
func (r *Runtime) sealedExtras(root string) ([]string, map[string]string) {
	resolved, err := r.resolve(root)
	if err != nil || resolved.Source == "builtin" {
		return nil, nil
	}
	test := resolved.Profile.Workflows["test"]
	return test.RuntimePaths, test.Env
}

// sealedProjectRoot accepts canonical immediate repositories and ONLY nested
// worktrees whose .git link resolves to a real immediate /root/repos Git
// repository. This prevents a nested arbitrary path from acquiring a sealed
// executor binding (MCP-232); Plan separately verifies clean HEAD and aliases.
func sealedProjectRoot(root string) bool {
	if filepath.Clean(root) != root {
		return false
	}
	if filepath.Dir(root) == "/root/repos" {
		return true
	}
	return repopath.Worktree(root) != ""
}

// sealedSnapshotRepository leaves the worktree as the job scope while the
// broker reads the pinned commit from its canonical object store.
func sealedSnapshotRepository(ctx context.Context, root string) (string, error) {
	if filepath.Dir(root) == "/root/repos" {
		return root, nil
	}
	repo := repopath.Worktree(root)
	resolved, err := filepath.EvalSymlinks(repo)
	if repo == "" || err != nil || resolved != repo {
		return "", errors.New("linked worktree repository is not canonical: HOLD")
	}
	common, err := safeGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common != filepath.Join(repo, ".git") {
		return "", errors.New("linked worktree object store changed: HOLD")
	}
	return repo, nil
}

// sealedArgv admits exactly the resolved profile's test workflow. The builtin
// DIAG profile keeps its legacy Make snapshot (nil argv); a registered or
// repository profile seals the whole tree with its own argv.
func (r *Runtime) sealedArgv(command string, args []string, root string) ([]string, bool) {
	if command == StageCommand || command == WorkflowCommand {
		if len(args) != 3 || !sealedProjectRoot(root) || r.resolve == nil {
			return nil, false
		}
		if command == StageCommand && (filepath.Dir(root) != "/root/repos" || !projectprofile.ValidStage(args[0])) {
			return nil, false
		}
		resolved, err := r.resolve(root)
		if err != nil || projectprofile.ProfileDigest(resolved.Profile) != args[1] {
			return nil, false
		}
		w, ok := resolved.Profile.Workflows[args[0]]
		if r.policy != nil {
			if _, denied := r.policy.DeniedCommands[filepath.Base(w.Command)]; denied {
				return nil, false
			}
		}
		argv := w.SealedArgv(args[0])
		return argv, ok && executor.ValidTreeCommand(argv)
	}
	// Profile resolution probes Git without hook hardening; reject scope first.
	if !sealedProjectRoot(root) || r.resolve == nil {
		return nil, false
	}
	resolved, err := r.resolve(root)
	if err != nil {
		return nil, false
	}
	test, ok := resolved.Profile.Workflows["test"]
	argv := test.SealedArgv("test")
	if !ok || (!slices.Equal(args, test.Args) && !slices.Equal(args, argv[1:])) {
		return nil, false
	}
	if resolved.Source == "builtin" {
		legacy := resolved.Profile.Name == "diag" && (command == "make" || command == "/usr/bin/make") && test.Command == "make" && len(args) == 1 && args[0] == "test"
		return nil, legacy
	}
	return argv, command == test.Command && executor.ValidTreeCommand(argv)
}
func bootstrap(b binding) string {
	fields := []any{b.Schema, b.JobID, b.Root, b.Commit, b.TimeoutSeconds, b.StartedAt}
	if b.Stage != "" {
		fields = append(fields, b.Stage, b.Artifacts)
	}
	if len(b.Argv) > 0 {
		fields = append(fields, b.Argv)
	}
	if len(b.Paths) > 0 || len(b.Env) > 0 {
		fields = append(fields, b.Paths, b.Env)
	}
	raw, _ := json.Marshal(fields)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// source is the sealed snapshot kind and broker argument tail of a binding.
func (b binding) source() (string, []string) {
	if len(b.Argv) == 0 {
		return "make:test", nil
	}
	return executor.TreeTestPackage, b.Argv
}
func (b binding) recipeArgs(action string) []string {
	_, argv := b.source()
	args := []string{action, b.Bootstrap, b.Root, b.Commit, strconv.FormatInt(b.TimeoutSeconds, 10)}
	extras := recipecontract.EncodeRecipeExtras(b.Paths, b.Env)
	if b.Stage != "" {
		raw, _ := json.Marshal(recipecontract.RecipeExtras{Paths: b.Paths, Env: b.Env, Stage: b.Stage, Artifacts: b.Artifacts})
		extras = "spec=" + base64.RawURLEncoding.EncodeToString(raw)
	}
	if extras != "" {
		args = append(args, extras)
	}
	return append(args, argv...)
}
func decode(raw []byte, recoverLegacy bool) (binding, error) {
	var b binding
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16384 || d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF || b.Schema != runtimeName || b.JobID == "" || len(b.JobID) > 64 || len(b.Commit) != 40 || b.TimeoutSeconds < 1 || b.TimeoutSeconds > budget.CeilingSeconds || b.StartedAt.IsZero() || bootstrap(b) != b.Bootstrap || (len(b.Argv) > 0 && !executor.ValidTreeCommand(b.Argv)) {
		return b, errors.New("invalid Workbench binding: HOLD")
	}
	// New bindings point only to canonical immediate snapshot repositories.
	// Recovering an old job must survive disappearance of its checkout. The
	// root broker still verifies its original permit and constructor identity.
	legacy := repopath.IsManagedWorktreePath(b.Root)
	if filepath.Clean(b.Root) != b.Root || (filepath.Dir(b.Root) != "/root/repos" && !(recoverLegacy && legacy)) {
		return b, errors.New("invalid Workbench repository binding: HOLD")
	}
	if b.Stage != "" && (!projectprofile.ValidStage(b.Stage) || projectprofile.ValidateArtifacts(b.Artifacts) != nil || (len(b.Artifacts) > 0 && b.Stage != "package")) {
		return b, errors.New("invalid stage binding")
	}
	commit, err := hex.DecodeString(b.Commit)
	if err != nil || len(commit) != 20 || hex.EncodeToString(commit) != b.Commit {
		return b, errors.New("invalid source identity")
	}
	pin, launch, err := recipecontract.RecipePrepareIdentity(b.Bootstrap)
	if err != nil || pin != b.Pin || launch != b.Launch {
		return b, errors.New("runtime identity changed: HOLD")
	}
	return b, nil
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(v []byte) (int, error) {
	if b.Len()+len(v) > 16384 {
		return 0, errors.New("Git evidence exceeds budget")
	}
	return b.Buffer.Write(v)
}
func safeGit(ctx context.Context, root string, args ...string) (string, error) {
	fixed := []string{"--no-replace-objects", "--no-lazy-fetch", "-c", "safe.directory=" + root, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-C", root}
	c := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
	c.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1", "LANG=C"}
	var out cappedBuffer
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// dirtyTree is a refusal before any effect, not a maybe-written HOLD: it read
// as FENCED_UNKNOWN/HOLD_REASON_UNCLASSIFIED and stopped agents (MCP-105).
func dirtyTree(root string, cause error) error {
	e := failure.Rejected("WORKTREE_DIRTY", "workbenchruntime.Plan", "commit the exact test inputs before Workbench admission; nothing was started — project_commit the changed files under your lease (git_status lists them), then repeat project_test", cause)
	// The next step is the commit itself: a client that only saw git_status
	// stopped after a blocked commit instead of retrying it (13.11, Lane G0).
	e.NextSafeAction = failure.Action{Tool: "project_commit", Arguments: map[string]any{"path": root}}
	return e
}

func (r *Runtime) Plan(id, command string, args []string, root string, opts jobs.StartOptions) (json.RawMessage, error) {
	argv, ok := r.sealedArgv(command, args, root)
	if !ok || !sealedProjectRoot(root) {
		return nil, errors.New("unsupported sealed recipe scope")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, errors.New("repository alias unavailable: HOLD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := projectlease.ValidateGitStatusInputs(ctx, root); err != nil {
		return nil, err
	}
	top, err := safeGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil || top != root {
		return nil, errors.New("canonical Git root required")
	}
	dirty, err := safeGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil || dirty != "" {
		return nil, dirtyTree(root, err)
	}
	commit, err := safeGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	repo, err := sealedSnapshotRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	b := binding{Schema: runtimeName, JobID: id, Root: repo, Commit: commit, TimeoutSeconds: int64(opts.Timeout / time.Second), StartedAt: r.now().UTC(), Argv: argv}
	if len(argv) > 0 {
		b.Paths, b.Env = r.sealedExtras(root)
	}
	if command == StageCommand || command == WorkflowCommand {
		if b.Commit != args[2] {
			return nil, errors.New("pipeline HEAD changed during admission")
		}
		resolved, err := r.resolve(root)
		if err != nil {
			return nil, err
		}
		w := resolved.Profile.Workflows[args[0]]
		if projectprofile.ProfileDigest(resolved.Profile) != args[1] || !slices.Equal(argv, w.SealedArgv(args[0])) {
			return nil, errors.New("profile changed during admission")
		}
		if command == StageCommand {
			b.Stage = args[0]
		}
		b.Paths = w.RuntimePaths
		b.Env = w.Env
		if b.Stage == "package" {
			b.Artifacts = w.Artifacts
			if len(b.Artifacts) == 0 {
				return nil, errors.New("package workflow must declare artifacts")
			}
		}
	}
	b.Bootstrap = bootstrap(b)
	b.Pin, b.Launch, err = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	_, err = decode(raw, false)
	return raw, err
}
func (r *Runtime) broker(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.call(ctx, "executor", args...)
}
func (r *Runtime) Start(raw json.RawMessage) error {
	b, err := decode(raw, false)
	if err != nil {
		return err
	}
	data, err := r.broker(50*time.Second, b.recipeArgs("prepare-recipe")...)
	if err != nil {
		return err
	}
	var pending recipecontract.RecipePrepareState
	if json.Unmarshal(data, &pending) == nil && pending.State != "" {
		if (pending.State == "PREPARING" || pending.State == "SEALED") && pending.PermitID == b.Pin.PermitID && pending.LaunchID == b.Launch {
			return nil // Observe waits for the sealed source before any launch.
		}
		return errors.New("sealed preparation scheduling differs: UNKNOWN")
	}
	pkg, _ := b.source()
	var m executor.Manifest
	if json.Unmarshal(data, &m) != nil || m.Pin != b.Pin || m.Purpose != "project-recipe-test-v1" || m.Source == nil || m.Source.Repo != b.Root || m.Source.Commit != b.Commit || m.Source.Package != pkg || len(m.Runs) != 1 || m.Runs[b.Launch].TimeoutSeconds != b.TimeoutSeconds {
		return errors.New("sealed preparation differs: UNKNOWN")
	}
	_, err = r.broker(15*time.Second, "launch", b.Pin.PermitID, b.Launch)
	return err
}
func (r *Runtime) status(b binding) (executor.Snapshot, error) {
	var s executor.Snapshot
	data, err := r.broker(10*time.Second, "status", b.Pin.PermitID, b.Launch)
	if err != nil {
		return s, errors.Join(errStatusUnavailable, err)
	}
	pkg, _ := b.source()
	if json.Unmarshal(data, &s) != nil || s.Pin != b.Pin || s.Source == nil || s.Source.Repo != b.Root || s.Source.Commit != b.Commit || s.Source.Package != pkg || len(s.Launches) > 1 {
		return s, errors.New("root executor observation differs: HOLD")
	}
	for _, l := range s.Launches {
		if l.ID != b.Launch {
			return s, errors.New("foreign launch observation: HOLD")
		}
	}
	return s, nil
}

// prepareWindow outlasts the bounded scheduling RPC (50s client) plus the
// constructor unit (budget + 5 s RuntimeMaxSec, TimeoutStopSec=5s), after
// which no unobserved permit can still appear.
var prepareWindow = 50*time.Second + budget.PhaseMax(recipecontract.RecipePrepareDefault) + 10*time.Second + time.Minute

func prepareWindowFor(b binding) time.Duration {
	return 50*time.Second + budget.PhaseMax(recipecontract.RecipePrepareBudget(len(b.Argv) > 0)) + 10*time.Second + time.Minute
}

var errStatusUnavailable = errors.New("executor status unavailable")

// abandoned turns a preparation that never sealed a permit into a verified
// failure; any other status error stays unresolved.
func (r *Runtime) abandoned(b binding, statusErr error) (jobs.ExternalObservation, error) {
	if r.now().Before(b.StartedAt.Add(prepareWindowFor(b))) {
		return jobs.ExternalObservation{}, statusErr
	}
	data, err := r.broker(30*time.Second, b.recipeArgs("abandon-recipe")...)
	var out struct {
		State    string `json:"state"`
		PermitID string `json:"permit_id"`
	}
	if err != nil || json.Unmarshal(data, &out) != nil || out.State != "ABANDONED" || out.PermitID != b.Pin.PermitID {
		// Say why the proven-dead preparation could not be closed: a silent
		// UNKNOWN hid a stale 1800 s check in abandon (BUG-20261103-24).
		why := "abandon answered " + out.State
		if err != nil {
			why = "abandon refused: " + err.Error()
		}
		return jobs.ExternalObservation{}, fmt.Errorf("%w; preparation never sealed a permit and %s", statusErr, why)
	}
	code := 1
	failed := jobs.ExternalObservation{State: jobs.Failed, ExitCode: &code, ResourcesReleased: true, Error: "sealed preparation failed before any permit; nothing was launched"}
	if repopath.IsManagedWorktreePath(b.Root) || b.Attempt+1 >= maxPrepareAttempts {
		failed.Error += fmt.Sprintf(" (after %d attempts)", b.Attempt+1)
		return failed, nil
	}
	// Root proved nothing ran, so preparing again is safe: same job, same
	// source, a fresh identity. The usual cause is a loaded host.
	next := b
	next.Attempt++
	next.StartedAt = r.now().UTC()
	next.Bootstrap = bootstrap(next)
	if next.Pin, next.Launch, err = recipecontract.RecipePrepareIdentity(next.Bootstrap); err != nil {
		return failed, nil
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return failed, nil
	}
	if err := r.Start(raw); err != nil {
		failed.Error += "; automatic re-preparation refused: " + err.Error()
		return failed, nil
	}
	return jobs.ExternalObservation{State: jobs.Running, Rebind: raw, Error: fmt.Sprintf("preparation re-attempt %d/%d after a no-effect failure", next.Attempt+1, maxPrepareAttempts)}, nil
}

// progressWindow: CPU must have advanced this recently to extend a budget.
const progressWindow = 5 * time.Minute

func (r *Runtime) progressing(launch string) bool {
	unit := "mcpshka-executor-" + launch + ".service"
	read := r.cpuUsage
	if read == nil {
		read = unitCPU
	}
	nsec, err := read(unit)
	if err != nil {
		return false
	}
	now := r.now()
	v, _ := r.progress.LoadOrStore(launch, &cpuSample{})
	c := v.(*cpuSample)
	c.mu.Lock()
	defer c.mu.Unlock()
	if nsec > c.nsec {
		c.nsec, c.advanced = nsec, now
	}
	return !c.advanced.IsZero() && now.Sub(c.advanced) < progressWindow
}

func unitCPU(unit string) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=CPUUsageNSec", "--value", unit).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
}

func (r *Runtime) Observe(raw json.RawMessage, cancel bool) (jobs.ExternalObservation, error) {
	b, err := decode(raw, true)
	if err != nil {
		return jobs.ExternalObservation{}, err
	}
	s, err := r.status(b)
	if repopath.IsManagedWorktreePath(b.Root) {
		if !errors.Is(err, errStatusUnavailable) {
			return jobs.ExternalObservation{}, errors.New("legacy nested preparation has conflicting permit evidence: HOLD")
		}
		return r.abandoned(b, err)
	}
	if err != nil {
		if errors.Is(err, errStatusUnavailable) && r.now().Before(b.StartedAt.Add(prepareWindowFor(b))) {
			data, probeErr := r.broker(10*time.Second, b.recipeArgs("recipe-status")...)
			var pending recipecontract.RecipePrepareState
			if probeErr == nil && json.Unmarshal(data, &pending) == nil && pending.PermitID == b.Pin.PermitID && pending.LaunchID == b.Launch && (pending.State == "PREPARING" || pending.State == "SEALED") {
				return jobs.ExternalObservation{State: jobs.Running}, nil
			}
		}
		return r.abandoned(b, err)
	}
	started := b.StartedAt
	// Root records permit creation after snapshot preparation. Keep the legacy
	// fallback for old receipts, then use the durable one-use launch time.
	if !s.CreatedAt.IsZero() {
		started = s.CreatedAt
	}
	for _, launch := range s.Launches {
		if !launch.ReservedAt.IsZero() {
			started = launch.ReservedAt
		}
	}
	if s.Queue != nil && s.Queue.RunningAt.After(started) {
		started = s.Queue.RunningAt
	}
	expired := !r.now().Before(started.Add(time.Duration(b.TimeoutSeconds) * time.Second))
	// Like every other job, a suite still burning CPU at its budget runs on,
	// up to the one ceiling (08.11: a 34-minute full run was cut at 1800 s).
	if expired && !cancel && s.Result == nil && r.now().Before(started.Add(budget.Ceiling)) && r.progressing(b.Launch) {
		expired = false
	}
	if s.Queue != nil && s.Queue.State == "QUEUED" {
		expired = false
	}
	if s.State == "ACTIVE" && len(s.Launches) == 0 && !cancel && !expired {
		// A sealed permit without a reservation proves no launch effect exists.
		// Reserve is an atomic one-use root CAS, including across two observers.
		if _, err := r.broker(15*time.Second, "launch", b.Pin.PermitID, b.Launch); err != nil {
			return jobs.ExternalObservation{}, err
		}
		return jobs.ExternalObservation{State: jobs.Running}, nil
	}
	if s.State != "FENCED" && (cancel || expired || s.Result != nil || s.State == "REVOKING") {
		action := "canary-fence"
		if s.State == "REVOKING" {
			action = "canary-recover"
		}
		if _, err := r.broker(15*time.Second, action, b.Pin.PermitID, b.Launch); err != nil {
			return jobs.ExternalObservation{}, err
		}
		s, err = r.status(b)
		if err != nil {
			return jobs.ExternalObservation{}, err
		}
	}
	if s.State != "FENCED" {
		if s.Queue != nil && s.Queue.State == "QUEUED" {
			return jobs.ExternalObservation{State: jobs.Queued, Queue: s.Queue}, nil
		}
		return jobs.ExternalObservation{State: jobs.Running, Queue: s.Queue, Unit: "mcpshka-executor-" + b.Launch + ".service"}, nil
	}
	if s.Result == nil {
		// This is failure of the controlling job, not an invented project exit.
		code := 1
		state := jobs.Failed
		if cancel {
			code = 130
			state = jobs.Cancelled
		} else if expired {
			code = 124
			state = jobs.TimedOut
		}
		return jobs.ExternalObservation{State: state, ExitCode: &code, ResourcesReleased: true, Error: "root fence verified; project exit result unavailable"}, nil
	}
	result := s.Result
	if result.Pin != b.Pin || result.LaunchID != b.Launch || (result.State != "SUCCEEDED" && result.State != "FAILED") {
		return jobs.ExternalObservation{}, errors.New("unverified execution result: HOLD")
	}
	state := jobs.Failed
	if result.State == "SUCCEEDED" && result.ExitCode == 0 && !result.WaitFailed {
		state = jobs.Done
	}
	code := result.ExitCode
	if state == jobs.Failed && code == 0 {
		code = 1
	}
	if state == jobs.Failed && code == 124 {
		state = jobs.TimedOut // the worker stopped the suite at its budget
	}
	return jobs.ExternalObservation{State: state, ExitCode: &code, ResourcesReleased: true, Output: result.Output}, nil
}
