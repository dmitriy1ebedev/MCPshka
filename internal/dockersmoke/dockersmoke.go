package dockersmoke

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/boundedbuf"
	"mcpshka/internal/executor"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/releaseapply"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Packaging smoke for a project's own Docker image (DIAG P12). MCPshka never
// joins the docker group: that is root. Root builds only the committed tree
// of a repository under /root/repos (read by git as the repository owner,
// streamed as the build context) and runs the image in fixed hardened
// profiles as uid 65532: read-only root, no capabilities, no-new-privileges,
// small memory/pid/CPU limits, no network unless the host-observer profile
// shares the host's namespaces read-only. Image, volume and containers of a
// request are removed when it ends.
const (
	// The broker may write only under the executor authority root.
	dockerSmokeRoot       = executor.AuthorityRoot + "/docker-smoke"
	dockerSmokeMaxRuns    = 10
	dockerSmokeRunTimeout = 90 * time.Second
	dockerSmokeBuildLimit = 20 * time.Minute
	dockerSmokeQueueLimit = 2 * time.Hour
	dockerSmokeContextMax = 512 << 20
	dockerSmokeStdoutMax  = 8 << 10
	dockerSmokeStderrMax  = 1 << 10
)

// Helper is the root-owned broker binary that runs the worker; RootOwned
// is the broker's check of it (unwired, it refuses).
const Helper = "/usr/local/libexec/mcpshka-broker"

var RootOwned = func(string) error { return errors.New("docker smoke: helper check not wired") }

var (
	repoPattern           = regexp.MustCompile(`^/root/repos/[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	commitPattern         = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dockerSmokeID         = regexp.MustCompile(`^[0-9a-f]{32}$`)
	dockerSmokeFile       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,199}$`)
	dockerSmokeKey        = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	dockerSmokeArgValue   = regexp.MustCompile(`^[A-Za-z0-9._:+/@-]{0,128}$`)
	dockerSmokeRunName    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	dockerSmokeEntrypoint = regexp.MustCompile(`^(/bin/sh|/usr/local/bin/[a-z0-9_-]{1,64})$`)
	dockerSmokeMountPath  = regexp.MustCompile(`^/[a-z0-9_.-]+(/[a-z0-9_.-]+){0,7}$`)
)

type DockerSmokeRequest struct {
	Repo       string            `json:"repo"`
	Commit     string            `json:"commit"`
	Dockerfile string            `json:"dockerfile"`
	BuildArgs  map[string]string `json:"build_args,omitempty"`
	Runs       []DockerSmokeRun  `json:"runs"`
}

type DockerSmokeRun struct {
	Name       string            `json:"name"`
	Profile    string            `json:"profile"`
	Entrypoint string            `json:"entrypoint,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Volume     string            `json:"volume,omitempty"`
}

type DockerSmokeRunResult struct {
	Name     string  `json:"name"`
	ExitCode int     `json:"exit_code"`
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr,omitempty"`
	TimedOut bool    `json:"timed_out,omitempty"`
	Seconds  float64 `json:"seconds"`
}

type DockerSmokeResult struct {
	ID         string                 `json:"id"`
	State      string                 `json:"state"`
	Repo       string                 `json:"repo"`
	Commit     string                 `json:"commit"`
	Image      string                 `json:"image,omitempty"`
	BuildLog   string                 `json:"build_log,omitempty"`
	Runs       []DockerSmokeRunResult `json:"runs,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Queue      *heavyqueue.Status     `json:"queue,omitempty"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
}

func (r DockerSmokeResult) terminal() bool { return r.State == "DONE" || r.State == "FAILED" }

func validateDockerSmoke(req DockerSmokeRequest) error {
	if !repoPattern.MatchString(req.Repo) || strings.Contains(req.Repo, "..") {
		return errors.New("docker smoke: repo must be /root/repos/<name>")
	}
	if !commitPattern.MatchString(req.Commit) {
		return errors.New("docker smoke: commit must be a full 40-hex SHA")
	}
	if !dockerSmokeFile.MatchString(req.Dockerfile) || strings.Contains(req.Dockerfile, "..") {
		return errors.New("docker smoke: dockerfile must be a relative path inside the repository")
	}
	if len(req.BuildArgs) > 8 {
		return errors.New("docker smoke: at most 8 build args")
	}
	for k, v := range req.BuildArgs {
		if !dockerSmokeKey.MatchString(k) || !dockerSmokeArgValue.MatchString(v) {
			return fmt.Errorf("docker smoke: build arg %q is not allowed", k)
		}
	}
	if len(req.Runs) == 0 || len(req.Runs) > dockerSmokeMaxRuns {
		return fmt.Errorf("docker smoke: 1..%d runs", dockerSmokeMaxRuns)
	}
	seen := map[string]bool{}
	for _, run := range req.Runs {
		if !dockerSmokeRunName.MatchString(run.Name) || seen[run.Name] {
			return fmt.Errorf("docker smoke: run name %q is invalid or repeated", run.Name)
		}
		seen[run.Name] = true
		if run.Profile != "isolated" && run.Profile != "host-observer" {
			return errors.New("docker smoke: profile must be isolated or host-observer")
		}
		if run.Entrypoint != "" && !dockerSmokeEntrypoint.MatchString(run.Entrypoint) {
			return errors.New("docker smoke: entrypoint must be /bin/sh or /usr/local/bin/<name>")
		}
		if run.Volume != "" && (!dockerSmokeMountPath.MatchString(run.Volume) || strings.Contains(run.Volume, "..")) {
			return errors.New("docker smoke: volume must be an absolute container path")
		}
		total := 0
		if len(run.Args) > 32 {
			return errors.New("docker smoke: at most 32 args per run")
		}
		for _, a := range run.Args {
			total += len(a)
			if strings.ContainsRune(a, 0) {
				return errors.New("docker smoke: args must not contain NUL")
			}
		}
		if total > 16<<10 {
			return errors.New("docker smoke: args exceed 16 KiB")
		}
		if len(run.Env) > 8 {
			return errors.New("docker smoke: at most 8 env values")
		}
		for k, v := range run.Env {
			if !dockerSmokeKey.MatchString(k) || len(v) > 256 || strings.ContainsAny(v, "\x00\n") {
				return fmt.Errorf("docker smoke: env %q is not allowed", k)
			}
		}
	}
	return nil
}

// The id binds the exact request, so a lost reply is answered by status and a
// repeated start returns the same run instead of building twice.
func dockerSmokeRequestID(req DockerSmokeRequest) string {
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

func dockerSmokeDir(id string) string { return filepath.Join(dockerSmokeRoot, id) }
func dockerSmokeUnit(id string) string {
	return "mcpshka-docker-smoke-" + id + ".service"
}

func Run(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("docker smoke: start <request JSON> or status <id>")
	}
	switch args[0] {
	case "status":
		if !dockerSmokeID.MatchString(args[1]) {
			return nil, errors.New("docker smoke: invalid id")
		}
		return dockerSmokeStatus(ctx, args[1])
	case "start":
		var req DockerSmokeRequest
		dec := json.NewDecoder(strings.NewReader(args[1]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return nil, fmt.Errorf("docker smoke: request: %w", err)
		}
		if err := validateDockerSmoke(req); err != nil {
			return nil, err
		}
		return startDockerSmoke(ctx, req)
	}
	return nil, errors.New("docker smoke: unknown action")
}

func startDockerSmoke(ctx context.Context, req DockerSmokeRequest) ([]byte, error) {
	id := dockerSmokeRequestID(req)
	dir := dockerSmokeDir(id)
	if _, err := os.Stat(filepath.Join(dir, "request.json")); err == nil {
		return dockerSmokeStatus(ctx, id)
	}
	if err := os.MkdirAll(dockerSmokeRoot, 0o700); err != nil {
		return nil, err
	}
	pruneDockerSmoke(time.Now())
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	raw, _ := json.Marshal(req)
	if err := releaseapply.WriteRootFile(filepath.Join(dir, "request.json"), raw, 0o600); err != nil {
		return nil, err
	}
	if err := saveDockerSmoke(DockerSmokeResult{ID: id, State: "STARTING", Repo: req.Repo, Commit: req.Commit, StartedAt: time.Now().UTC()}); err != nil {
		return nil, err
	}
	if err := RootOwned(Helper); err != nil {
		return nil, err
	}
	// The worker only talks to dockerd; dockerd's own cgroup bears the build,
	// so the heavy queue, not this unit's quota, keeps the host responsive.
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--unit="+dockerSmokeUnit(id), "--collect", "--no-block", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=10800s", "--property=TimeoutStopSec=20s", "--property=MemoryMax=256M", "--property=CPUQuota=25%", "--property=TasksMax=64",
		"--property=Nice=10", "--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=ProtectSystem=strict",
		"--property=ReadWritePaths="+dockerSmokeRoot+" "+heavyqueue.Directory+" /run/docker.sock",
		Helper, "docker-smoke-worker", id)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker smoke: start worker: %w: %.512s", err, out)
	}
	return dockerSmokeStatus(ctx, id)
}

func loadDockerSmoke(id string) (DockerSmokeResult, error) {
	var out DockerSmokeResult
	raw, err := os.ReadFile(filepath.Join(dockerSmokeDir(id), "result.json"))
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

func saveDockerSmoke(r DockerSmokeResult) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return releaseapply.WriteRootFile(filepath.Join(dockerSmokeDir(r.ID), "result.json"), raw, 0o600)
}

func dockerSmokeStatus(ctx context.Context, id string) ([]byte, error) {
	r, err := loadDockerSmoke(id)
	if os.IsNotExist(err) {
		return nil, errors.New("docker smoke: unknown id")
	}
	if err != nil {
		return nil, err
	}
	if !r.terminal() && !dockerSmokeAlive(r, time.Now(), UnitState(ctx, dockerSmokeUnit(id))) {
		// Re-read: the worker may have finished between the two reads.
		if r2, e := loadDockerSmoke(id); e == nil && r2.terminal() {
			r = r2
		} else {
			now := time.Now().UTC()
			r.State, r.Error, r.FinishedAt = "FAILED", "worker ended without a result (killed or timed out)", &now
		}
	}
	if r.State == "QUEUED" {
		r.Queue, _ = heavyqueue.Host().Lookup("docker-smoke:" + id)
	}
	return json.Marshal(r)
}

// StartGrace covers the gap between systemd-run --no-block and
// the unit becoming visible to systemctl.
const StartGrace = 2 * time.Minute

// dockerSmokeAlive reports whether a non-terminal result still has a worker:
// systemd's transitional states count as alive (BUG-107: activating was read
// as a dead worker right after start), and so does a fresh STARTING record
// whose unit systemd has not loaded yet.
func dockerSmokeAlive(r DockerSmokeResult, now time.Time, state string) bool {
	switch state {
	case "active", "activating", "reloading", "refreshing", "deactivating":
		return true
	}
	return r.State == "STARTING" && now.Sub(r.StartedAt) < StartGrace
}

// unitState returns systemctl is-active's word for unit ("" when unknown);
// is-active exits non-zero for every state but active, so only stdout counts.
// UnitState is systemctl is-active of unit ("" when unknown).
func UnitState(ctx context.Context, unit string) string {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", unit)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// Results are small; keep a week of them for evidence and forget the rest.
func pruneDockerSmoke(now time.Time) {
	entries, _ := os.ReadDir(dockerSmokeRoot)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && e.IsDir() && dockerSmokeID.MatchString(e.Name()) && now.Sub(info.ModTime()) > 7*24*time.Hour {
			os.RemoveAll(filepath.Join(dockerSmokeRoot, e.Name()))
		}
	}
}

// RunDockerSmokeWorker runs inside the transient unit started above.
func RunWorker(args []string) error {
	if os.Geteuid() != 0 || len(args) != 1 || !dockerSmokeID.MatchString(args[0]) {
		return errors.New("docker smoke worker requires root and an id")
	}
	id := args[0]
	raw, err := os.ReadFile(filepath.Join(dockerSmokeDir(id), "request.json"))
	if err != nil {
		return err
	}
	var req DockerSmokeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	res, err := loadDockerSmoke(id)
	if err != nil {
		return err
	}
	finish := func(state, msg string) error {
		now := time.Now().UTC()
		res.State, res.Error, res.FinishedAt, res.Queue = state, msg, &now, nil
		return saveDockerSmoke(res)
	}
	if err := validateDockerSmoke(req); err != nil {
		return finish("FAILED", err.Error())
	}
	res.State = "QUEUED"
	saveDockerSmoke(res)
	ticket, err := heavyqueue.Host().EnqueueScoped("docker-smoke:"+id, req.Repo)
	if err != nil {
		return finish("FAILED", "heavy queue: "+err.Error())
	}
	defer ticket.Close()
	wait, cancel := context.WithTimeout(context.Background(), dockerSmokeQueueLimit)
	err = ticket.Wait(wait, nil)
	cancel()
	if err != nil {
		return finish("FAILED", "heavy queue: "+err.Error())
	}
	image := "mcpshka-smoke:" + id
	volume := "mcpshka-smoke-" + id
	env := dockerSmokeEnv(id)
	defer dockerSmokeCleanup(id, image, volume, env)
	res.State, res.Image = "BUILDING", image
	saveDockerSmoke(res)
	log, err := dockerSmokeBuild(req, id, image, env)
	res.BuildLog = log
	if err != nil {
		return finish("FAILED", err.Error())
	}
	res.State = "RUNNING"
	saveDockerSmoke(res)
	for i, run := range req.Runs {
		if run.Volume != "" {
			if out, err := dockerCommand(context.Background(), env, nil, "volume", "create", "--label", "mcpshka.smoke="+id, volume); err != nil {
				return finish("FAILED", "volume: "+tail(out, 512))
			}
		}
		res.Runs = append(res.Runs, dockerSmokeRunOne(id, i, image, volume, run, env))
		saveDockerSmoke(res)
	}
	return finish("DONE", "")
}

// The docker CLI gets an empty config dir: no root registry credentials leak
// into builds, and pulls of public base images stay anonymous.
func dockerSmokeEnv(id string) []string {
	cfg := filepath.Join(dockerSmokeDir(id), "docker-config")
	os.MkdirAll(cfg, 0o700)
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + cfg, "DOCKER_CONFIG=" + cfg, "DOCKER_BUILDKIT=1"}
}

func dockerCommand(ctx context.Context, env []string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/docker", args...)
	cmd.Env, cmd.Stdin = env, stdin
	out := &boundedbuf.Buffer{Limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return string(out.Bytes()), err
}

func dockerSmokeBuild(req DockerSmokeRequest, id, image string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerSmokeBuildLimit)
	defer cancel()
	if err := repoGit(ctx, req.Repo, nil, "cat-file", "-e", req.Commit+"^{commit}"); err != nil {
		return "", errors.New("commit is not in the repository")
	}
	if err := repoGit(ctx, req.Repo, nil, "cat-file", "-e", req.Commit+":"+req.Dockerfile); err != nil {
		return "", errors.New("dockerfile is not in that commit")
	}
	pr, pw := io.Pipe()
	gitDone := make(chan error, 1)
	go func() {
		err := repoGit(ctx, req.Repo, &capWriter{w: pw, left: dockerSmokeContextMax}, "archive", "--format=tar", req.Commit)
		pw.CloseWithError(err)
		gitDone <- err
	}()
	args := []string{"build", "--progress=plain", "--iidfile", filepath.Join(dockerSmokeDir(id), "iid"),
		"-f", req.Dockerfile, "-t", image, "--label", "mcpshka.smoke=" + id}
	keys := make([]string, 0, len(req.BuildArgs))
	for k := range req.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+req.BuildArgs[k])
	}
	out, err := dockerCommand(ctx, env, pr, append(args, "-")...)
	pr.Close()
	gitErr := <-gitDone
	if gitErr != nil && err == nil {
		err = gitErr
	}
	if ctx.Err() != nil {
		return tail(out, 3<<10), fmt.Errorf("build exceeded %s", dockerSmokeBuildLimit)
	}
	if err != nil {
		return tail(out, 3<<10), fmt.Errorf("build failed: %v", err)
	}
	return tail(out, 3<<10), nil
}

// git reads the repository as its owner, never as root: a hostile config,
// attribute filter or hook cannot run with root rights.
func repoGit(ctx context.Context, repo string, stdout io.Writer, args ...string) error {
	info, err := os.Lstat(repo)
	if err != nil || !info.IsDir() {
		return errors.New("repository is not a directory")
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid == 0 {
		return errors.New("refusing a root-owned repository")
	}
	full := append([]string{"-C", repo, "-c", "core.fsmonitor=false", "-c", "safe.directory=" + repo}, args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/git", full...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, Groups: []uint32{}}}
	cmd.Stdout = stdout
	return cmd.Run()
}

type capWriter struct {
	w    io.Writer
	left int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errors.New("build context exceeds 512 MiB")
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

func dockerSmokeRunArgs(id string, i int, image, volume string, run DockerSmokeRun) []string {
	args := []string{"run", "--rm", "--name", fmt.Sprintf("mcpshka-smoke-%s-%d", id, i), "--label", "mcpshka.smoke=" + id,
		"--pull", "never", "--user", "65532:65532", "--read-only", "--tmpfs", "/tmp:size=16m,mode=1777",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64",
		"--memory", "96m", "--memory-swap", "96m", "--cpus", "0.10", "--stop-timeout", "5"}
	if run.Profile == "host-observer" {
		args = append(args, "--pid", "host", "--network", "host", "--cgroupns", "host",
			"--mount", "type=bind,src=/sys,dst=/sys,readonly",
			"--mount", "type=bind,src=/sys,dst=/host/sys,readonly",
			"--mount", "type=bind,src=/etc/os-release,dst=/host/os-release,readonly",
			"--mount", "type=bind,src=/proc/1/ns/pid,dst=/host/ns/pid,readonly",
			"--mount", "type=bind,src=/proc/1/ns/net,dst=/host/ns/net,readonly",
			"--mount", "type=bind,src=/proc/1/ns/cgroup,dst=/host/ns/cgroup,readonly")
	} else {
		args = append(args, "--network", "none")
	}
	if run.Volume != "" {
		args = append(args, "--mount", "type=volume,src="+volume+",dst="+run.Volume)
	}
	keys := make([]string, 0, len(run.Env))
	for k := range run.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+run.Env[k])
	}
	if run.Entrypoint != "" {
		args = append(args, "--entrypoint", run.Entrypoint)
	}
	return append(append(args, image), run.Args...)
}

func dockerSmokeRunOne(id string, i int, image, volume string, run DockerSmokeRun, env []string) DockerSmokeRunResult {
	ctx, cancel := context.WithTimeout(context.Background(), dockerSmokeRunTimeout)
	defer cancel()
	start := time.Now()
	cmd := exec.Command("/usr/bin/docker", dockerSmokeRunArgs(id, i, image, volume, run)...)
	cmd.Env = env
	stdout, stderr := &boundedbuf.Buffer{Limit: dockerSmokeStdoutMax}, &boundedbuf.Buffer{Limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	out := DockerSmokeRunResult{Name: run.Name, ExitCode: -1}
	if err := cmd.Start(); err != nil {
		out.Stderr = err.Error()
		return out
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-ctx.Done():
		out.TimedOut = true
		// Killing the CLI would leave the container; remove it in dockerd.
		dockerCommand(context.Background(), env, nil, "rm", "-f", fmt.Sprintf("mcpshka-smoke-%s-%d", id, i))
		<-done
	}
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}
	out.Stdout, out.Stderr = string(stdout.Bytes()), tail(string(stderr.Bytes()), dockerSmokeStderrMax)
	out.Seconds = time.Since(start).Round(10 * time.Millisecond).Seconds()
	return out
}

func dockerSmokeCleanup(id, image, volume string, env []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := dockerCommand(ctx, env, nil, "ps", "-aq", "--filter", "label=mcpshka.smoke="+id); err == nil {
		if ids := strings.Fields(out); len(ids) > 0 {
			dockerCommand(ctx, env, nil, append([]string{"rm", "-f"}, ids...)...)
		}
	}
	dockerCommand(ctx, env, nil, "volume", "rm", "-f", volume)
	dockerCommand(ctx, env, nil, "image", "rm", "-f", image)
	os.RemoveAll(filepath.Join(dockerSmokeDir(id), "docker-config"))
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
