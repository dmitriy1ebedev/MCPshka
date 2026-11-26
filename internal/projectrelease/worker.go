package projectrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/unitbus"
)

// Input is the project_release tool input.
type Input struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	Path           string `json:"path" jsonschema:"Project under /root/repos (not MCPshka itself: it has release_apply)."`
	Commit         string `json:"commit" jsonschema:"Full 40-hex commit: on the project's main, checked out there, with passing CI."`
	Version        string `json:"version" jsonschema:"Version being released, e.g. 1.6.0."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type StatusInput struct {
	Path    string `json:"path" jsonschema:"Project under /root/repos."`
	Version string `json:"version" jsonschema:"Version passed to project_release."`
}

const (
	ApplyDescription  = "Release ANY project on the host from a commit to a verified install with one call, under the Face ID window: root runs the steps of the project's own recipe .mcpshka/release.json taken from that commit (schema mcpshka.release/v1: steps[{name, argv, root, timeout_seconds, expect, fail, retry_seconds, capture}], env, version_file, rollback, rollback_from — fixed argv, no shell, {commit} {version} {root} {name}, {artifact:repo-relative-path} from the verified package export of that commit, and captured values). The commit must be on main and checked out clean; a failing step at or after rollback_from runs rollback. A new or changed recipe answers RECIPE_NOT_APPROVED with the one Face ID plan that pins it. Adding a project = adding its recipe file, no MCPshka change. Returns STARTED; follow project_release_status."
	StatusDescription = "Progress and result of a project_release: RUNNING with the current step, INSTALLED, FAILED, ROLLED_BACK, ROLLBACK_FAILED, RECIPE_NOT_APPROVED or REFUSED, each step's outcome with its output tail, and the worker unit state. Before a release it shows whether the project's recipe on main is approved. Read-only."
)

func project(path string) (string, error) {
	name := filepath.Base(filepath.Clean(path))
	if filepath.Clean(path) != filepath.Join(ReposRoot, name) || strings.Contains(path, "..") || !nameRe.MatchString(name) || name == "mcpshka" {
		return "", errors.New("path must be /root/repos/<project> (MCPshka itself is released with release_apply)")
	}
	return name, nil
}

// BrokerArgs is the administrative-session call of one release.
func (in Input) BrokerArgs(owner string) ([]string, error) {
	name, err := project(in.Path)
	if err != nil {
		return nil, err
	}
	commit := strings.ToLower(in.Commit)
	if !validHex(commit, 40) || !versionRe.MatchString(in.Version) {
		return nil, errors.New("full 40-hex commit and a version like 1.6.0 are required")
	}
	return []string{"project-release", in.SessionID, owner, name, commit, in.Version}, nil
}

// Start runs in the root broker after it has checked the Face ID window:
// it only schedules the worker unit and returns.
func Start(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 6 || args[0] != "project-release" || !nameRe.MatchString(args[3]) || args[3] == "mcpshka" || !validHex(args[4], 40) || !versionRe.MatchString(args[5]) {
		return nil, errors.New("project-release requires session, owner, project, 40-hex commit and version")
	}
	unit := Unit(args[3], args[5])
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--unit="+unit, "--collect", "--no-block", "--quiet",
		"--property=Type=oneshot", "--property=User=root", "--property=Group=root", "--property=UMask=0022",
		"--property=RuntimeMaxSec=7500s", "--property=MemoryMax=2G", "--property=CPUQuota=200%", "--property=TasksMax=256",
		"--property=Nice=10", Broker, "project-release-worker", args[3], args[4], args[5])
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("project release not started (unit %s; a release of this version may be running): %.512s", unit, bytes.TrimSpace(out))
	}
	return json.Marshal(Result{Project: args[3], Version: args[5], Commit: args[4], Unit: unit, State: "STARTED", Next: "project_release_status"})
}

type worker struct {
	root string
	res  Result
	git  func(ctx context.Context, a ...string) ([]byte, error)
	// run executes one command; tests replace it.
	run   func(ctx context.Context, s Step, argv []string, env []string) ([]byte, error)
	sleep func(time.Duration)
	save  func(Result)
	// ci reads the project's CI profile (live by default); tests pin it so
	// the host's own /root/repos tree does not decide their outcome.
	ci func(root string) *projectprofile.CI
}

// RunWorker runs as root in its own unit and always leaves a result file.
func RunWorker(args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("project release worker must run as root")
	}
	if len(args) != 3 || !nameRe.MatchString(args[0]) || args[0] == "mcpshka" || !validHex(args[1], 40) || !versionRe.MatchString(args[2]) {
		return nil, errors.New("project release worker arguments")
	}
	root := filepath.Join(ReposRoot, args[0])
	w := &worker{root: root, res: Result{Project: args[0], Commit: args[1], Version: args[2], Unit: Unit(args[0], args[2]), State: "RUNNING"}, sleep: time.Sleep}
	w.git = func(ctx context.Context, a ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-c", "safe.directory=" + root, "-C", root}, a...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent"}
		return cmd.Output()
	}
	var owner syscall.Stat_t
	if err := syscall.Stat(root, &owner); err != nil {
		return nil, errors.New("project directory not found")
	}
	w.run = func(ctx context.Context, s Step, argv, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = root, env
		if !s.Root {
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: owner.Uid, Gid: owner.Gid}}
		}
		var out tailBuffer
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.WaitDelay = 10 * time.Second
		err := cmd.Run()
		return out.Bytes(), err
	}
	w.save = func(r Result) {
		raw, _ := json.Marshal(r)
		path := resultPath(r.Project, r.Version)
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil && os.MkdirAll(filepath.Dir(pinPath(r.Project)), 0o755) == nil {
			_ = writeFile(path, raw)
		}
	}
	lock, err := os.OpenFile(filepath.Join("/run", "mcpshka-project-release-"+args[0]+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		defer lock.Close()
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		w.res.State, w.res.Error = "REFUSED", "another release of this project is running"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 7200*time.Second)
		defer cancel()
		w.release(ctx)
	}
	w.res.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	w.save(w.res)
	raw, _ := json.Marshal(w.res)
	if w.res.State != "INSTALLED" {
		return raw, errors.New(w.res.State + ": " + w.res.Error)
	}
	return raw, nil
}

func (w *worker) refuse(state, msg string) {
	w.res.State, w.res.Error = state, msg
}

// release checks identity and approval, then runs the recipe.
func (w *worker) release(ctx context.Context) {
	res := &w.res
	if _, err := w.git(ctx, "merge-base", "--is-ancestor", res.Commit, "refs/heads/main"); err != nil {
		w.refuse("REFUSED", "commit is not on the project's main")
		return
	}
	head, _ := w.git(ctx, "rev-parse", "HEAD")
	dirty, err := w.git(ctx, "status", "--porcelain", "--untracked-files=no")
	if strings.TrimSpace(string(head)) != res.Commit || err != nil || len(bytes.TrimSpace(dirty)) != 0 {
		w.refuse("REFUSED", "the project checkout must be clean and exactly at the released commit (steps run in it)")
		return
	}
	raw, err := w.git(ctx, "show", res.Commit+":"+RecipePath)
	if err != nil {
		w.refuse("REFUSED", "the commit has no "+RecipePath+": describe the release there (schema "+Schema+")")
		return
	}
	recipe, err := ParseRecipe(raw)
	if err != nil {
		w.refuse("REFUSED", err.Error())
		return
	}
	if w.ci == nil {
		w.ci = projectprofile.New(policy.Default()).CIFor
	}
	if c := w.ci(w.root); c != nil && c.ExternalOnly {
		if err := externalRecipe(recipe); err != nil {
			w.refuse("REFUSED", err.Error())
			return
		}
	}
	res.RecipeSHA256 = Digest(raw)
	if pin, err := os.ReadFile(pinPath(res.Project)); err != nil || strings.TrimSpace(string(pin)) != res.RecipeSHA256 {
		w.refuse("RECIPE_NOT_APPROVED", "the release recipe of this commit is not the one the owner approved")
		res.Next = approveHint(res.Project, res.RecipeSHA256)
		return
	}
	if recipe.VersionFile != "" {
		decl, err := w.git(ctx, "show", res.Commit+":"+recipe.VersionFile)
		if s := strings.TrimSpace(string(decl)); err != nil || (s != res.Version && !strings.Contains(s, `"`+res.Version+`"`)) {
			w.refuse("REFUSED", "the commit's "+recipe.VersionFile+" does not declare version "+res.Version)
			return
		}
	}
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "HOME=/root"}
	for k, v := range recipe.Env {
		env = append(env, k+"="+v)
	}
	vars := map[string]string{"commit": res.Commit, "version": res.Version, "root": w.root, "name": res.Project}
	rollback := false
	for _, s := range recipe.Steps {
		rollback = rollback || s.Name == recipe.RollbackFrom
		res.Step = s.Name
		w.save(*res)
		out, err := w.step(ctx, s, vars, env)
		res.Steps = append(res.Steps, out)
		if err == nil {
			continue
		}
		res.State, res.Error = "FAILED", "step "+s.Name+": "+err.Error()
		if rollback && recipe.Rollback != nil {
			back, berr := w.step(ctx, *recipe.Rollback, vars, env)
			res.Steps = append(res.Steps, back)
			for _, then := range recipe.RollbackThen {
				if berr != nil {
					break
				}
				var out StepOutcome
				out, berr = w.step(ctx, then, vars, env)
				res.Steps = append(res.Steps, out)
			}
			if res.State = "ROLLED_BACK"; berr != nil {
				res.State, res.Error = "ROLLBACK_FAILED", res.Error+"; rollback: "+berr.Error()
			}
		}
		return
	}
	res.State, res.Step = "INSTALLED", ""
}

// step runs one command, repeating it while a retry step still waits.
func (w *worker) step(ctx context.Context, s Step, vars map[string]string, env []string) (StepOutcome, error) {
	out := StepOutcome{Name: s.Name}
	argv := make([]string, len(s.Argv))
	for i, a := range s.Argv {
		resolved, err := ExpandArtifacts(w.root, w.res.Commit, a)
		if err != nil {
			return out, err
		}
		argv[i] = expand(resolved, vars)
	}
	if strings.HasPrefix(argv[0], "./") {
		argv[0] = filepath.Join(w.root, argv[0])
		if !strings.HasPrefix(argv[0], w.root+"/") {
			return out, errors.New("argv[0] leaves the repository")
		}
	}
	timeout := time.Duration(s.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() { out.Seconds = int(time.Since(started).Seconds()) }()
	for {
		out.Tries++
		raw, err := w.run(ctx, s, argv, env)
		out.Tail = tail(raw, 1500)
		matched := s.Expect == "" || regexp.MustCompile(s.Expect).Match(raw)
		switch {
		case ctx.Err() != nil:
			return out, fmt.Errorf("timed out after %s", timeout)
		case s.Fail != "" && regexp.MustCompile(s.Fail).Match(raw):
			return out, errors.New("output reports a failure")
		case err == nil && matched:
			for k, expr := range s.Capture {
				m := regexp.MustCompile(expr).FindSubmatch(raw)
				if m == nil {
					return out, fmt.Errorf("output has no %s", k)
				}
				vars[k] = string(m[len(m)-1])
				if len(m) > 2 {
					vars[k] = string(m[1])
				}
			}
			out.OK = true
			return out, nil
		case s.RetrySeconds == 0 && err != nil:
			return out, fmt.Errorf("exit: %v", err)
		case s.RetrySeconds == 0:
			return out, errors.New("output does not match expect")
		}
		w.sleep(time.Duration(s.RetrySeconds) * time.Second)
	}
}

// tailBuffer keeps the last 256 KiB of a step's output.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - 256<<10; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}
func (t *tailBuffer) Bytes() []byte { return t.b }

func tail(raw []byte, n int) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	return strings.ToValidUTF8(string(raw), "?")
}

func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".project-release-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil || tmp.Chmod(0o644) != nil || tmp.Sync() != nil || tmp.Close() != nil {
		tmp.Close()
		return errors.New("write " + filepath.Base(path))
	}
	return os.Rename(tmp.Name(), path)
}

// RunPin records the owner's approval of the recipe now on the project's
// main. It runs only as root, which a chat reaches only through a
// plan-bound Face ID task showing this exact command line.
func RunPin(args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("project release pin must run as root")
	}
	if len(args) != 2 || !nameRe.MatchString(args[0]) || args[0] == "mcpshka" || !validHex(args[1], 64) {
		return nil, errors.New("project-release-pin requires project and recipe sha256")
	}
	root := filepath.Join(ReposRoot, args[0])
	cmd := exec.Command("/usr/bin/git", "-c", "safe.directory="+root, "-C", root, "show", "refs/heads/main:"+RecipePath)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent"}
	raw, err := cmd.Output()
	if err != nil {
		return nil, errors.New("main has no " + RecipePath)
	}
	if _, err := ParseRecipe(raw); err != nil {
		return nil, err
	}
	if Digest(raw) != args[1] {
		return nil, errors.New("the recipe on main is not the approved one: prepare the approval again")
	}
	if err := os.MkdirAll(filepath.Dir(pinPath(args[0])), 0o755); err != nil {
		return nil, err
	}
	if err := writeFile(pinPath(args[0]), []byte(args[1]+"\n")); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"project": args[0], "recipe_sha256": args[1], "state": "APPROVED"})
}

// Status reads the result as the unprivileged server. Before a release it
// reports whether the recipe on main is approved.
func Status(ctx context.Context, in StatusInput) (Result, error) {
	name, err := project(in.Path)
	if err != nil || !versionRe.MatchString(in.Version) {
		return Result{}, errors.New("path /root/repos/<project> and a version like 1.6.0 are required")
	}
	out := Result{Project: name, Version: in.Version, Unit: Unit(name, in.Version), State: "NOT_STARTED", UnitState: "unknown"}
	if raw, err := os.ReadFile(resultPath(name, in.Version)); err == nil && len(raw) < 256<<10 {
		var res Result
		if json.Unmarshal(raw, &res) == nil && res.Version == in.Version && res.Project == name {
			out = res
		}
	}
	show, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if raw, err := unitbus.Output(show, "show", "--property=ActiveState,Result", out.Unit+".service"); err == nil {
		out = reconcileUnit(out, raw)
	}
	if out.State == "NOT_STARTED" {
		root := filepath.Join(ReposRoot, name)
		raw, err := exec.CommandContext(show, "/usr/bin/git", "-c", "safe.directory="+root, "-C", root, "show", "refs/heads/main:"+RecipePath).Output()
		switch _, perr := ParseRecipe(raw); {
		case err != nil:
			out.Next = "main has no " + RecipePath + ": add the recipe (schema " + Schema + ", see project_release) and commit it"
		case perr != nil:
			out.Next = perr.Error()
		default:
			out.RecipeSHA256 = Digest(raw)
			out.Next = "project_release"
			if pin, err := os.ReadFile(pinPath(name)); err != nil || strings.TrimSpace(string(pin)) != out.RecipeSHA256 {
				out.Next = approveHint(name, out.RecipeSHA256) + " (первый project_release создаст каталог состояния)"
			}
		}
	}
	return out, nil
}

// Named properties avoid relying on systemctl's property output order.
func reconcileUnit(out Result, raw []byte) Result {
	props := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	active := props["ActiveState"]
	out.UnitState = active + "/" + props["Result"]
	switch active {
	case "active", "activating", "reloading", "deactivating":
		out.State = "RUNNING"
		out.Error = ""
	case "failed", "inactive":
		if out.State == "RUNNING" || out.State == "STARTED" {
			out.State = "FAILED"
			out.Error = "the worker unit ended without a result: check the step's real effect before another release"
		}
	}
	return out
}
