// Package containerctl is the owner's container control behind the root
// broker: the list is derived from live Docker (no hand-kept allowlist), the
// actions are start/stop/restart of an existing container, logs are a bounded
// tail. A start that Docker refuses because containerd left a dead task
// directory behind ("file exists", 08.11 Zigbee2MQTT) is healed once: the dead
// directory is moved aside (never deleted) and the start retried.
package containerctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Container is one row of the owner page.
type Container struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`  // running, exited, restarting, …
	Status   string `json:"status"` // Docker's human line: "Up 3 hours (healthy)"
	Health   string `json:"health,omitempty"`
	Restarts int    `json:"restarts"`
	Started  string `json:"started_at,omitempty"`
	Policy   string `json:"restart_policy,omitempty"`
	Compose  string `json:"compose_project,omitempty"`
	CPU      string `json:"cpu,omitempty"`
	Memory   string `json:"memory,omitempty"`
}

// Result is what an action returns: the container afterwards and whether a
// dead containerd task had to be moved aside.
type Result struct {
	Container Container `json:"container"`
	Healed    string    `json:"healed,omitempty"`
}

// Runner executes docker with args and returns combined output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

var (
	// TaskRoot and StaleRoot are the containerd v2 task directories (vars for tests).
	TaskRoot  = "/run/containerd/io.containerd.runtime.v2.task/moby"
	StaleRoot = "/run/containerd-stale"

	nameRE    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	staleRE   = regexp.MustCompile(`/moby/([0-9a-f]{64}): file exists`)
	ansiRE    = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	procAlive = func(pid int) bool { _, err := os.Stat("/proc/" + strconv.Itoa(pid)); return err == nil }
)

const (
	MaxLogLines = 300
	maxLogBytes = 48 << 10
)

// ValidName reports a syntactically valid container name.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// List returns every container on the host with health, restarts and usage.
func List(ctx context.Context, run Runner) ([]Container, error) { return list(ctx, run, true) }

func list(ctx context.Context, run Runner, withUsage bool) ([]Container, error) {
	raw, err := run(ctx, "ps", "-a", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var out []Container
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row struct{ Names, Image, State, Status string }
		if line == "" || json.Unmarshal([]byte(line), &row) != nil || !ValidName(row.Names) {
			continue
		}
		out = append(out, Container{Name: row.Names, Image: row.Image, State: row.State, Status: row.Status})
	}
	if len(out) == 0 {
		return out, nil
	}
	names := make([]string, len(out))
	for i, c := range out {
		names[i] = c.Name
	}
	details := inspect(ctx, run, names)
	usage := map[string][2]string{}
	if withUsage {
		usage = stats(ctx, run) // docker stats takes ~2 s; only the page needs it
	}
	for i := range out {
		if d, ok := details[out[i].Name]; ok {
			out[i].Health, out[i].Restarts, out[i].Started, out[i].Policy, out[i].Compose = d.Health, d.Restarts, d.Started, d.Policy, d.Compose
		}
		if u, ok := usage[out[i].Name]; ok {
			out[i].CPU, out[i].Memory = u[0], u[1]
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if (out[a].State == "running") != (out[b].State == "running") {
			return out[a].State != "running" // problems first
		}
		return out[a].Name < out[b].Name
	})
	return out, nil
}

const inspectFormat = `{"name":{{json .Name}},"restarts":{{.RestartCount}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"started":{{json .State.StartedAt}},"policy":{{json .HostConfig.RestartPolicy.Name}},"compose":{{json (index .Config.Labels "com.docker.compose.project")}}}`

type detail struct {
	Name, Health, Started, Policy, Compose string
	Restarts                               int
}

func inspect(ctx context.Context, run Runner, names []string) map[string]detail {
	out := map[string]detail{}
	raw, err := run(ctx, append([]string{"inspect", "--format", inspectFormat}, names...)...)
	if err != nil && len(raw) == 0 {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var d detail
		if json.Unmarshal([]byte(line), &d) == nil && d.Name != "" {
			out[strings.TrimPrefix(d.Name, "/")] = d
		}
	}
	return out
}

func stats(ctx context.Context, run Runner) map[string][2]string {
	out := map[string][2]string{}
	raw, err := run(ctx, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var row struct{ Name, CPUPerc, MemUsage string }
		if json.Unmarshal([]byte(line), &row) == nil && row.Name != "" {
			out[row.Name] = [2]string{row.CPUPerc, row.MemUsage}
		}
	}
	return out
}

// Act runs start, stop or restart on a container that exists right now.
func Act(ctx context.Context, run Runner, name, action string) (Result, error) {
	switch action {
	case "start", "stop", "restart":
	default:
		return Result{}, fmt.Errorf("unsupported action %q", action)
	}
	c, err := find(ctx, run, name)
	if err != nil {
		return Result{}, err
	}
	args := []string{action, name}
	if action != "start" {
		args = []string{action, "--time", "10", name}
	}
	var res Result
	raw, err := run(ctx, args...)
	if err != nil && action != "stop" {
		if moved, ok := healStaleTask(string(raw)); ok {
			res.Healed = moved
			raw, err = run(ctx, args...)
		}
	}
	if err != nil {
		return res, fmt.Errorf("docker %s %s: %w: %s", action, name, err, bounded(raw, 512))
	}
	if after, err := find(ctx, run, name); err == nil {
		c = after
	}
	res.Container = c
	return res, nil
}

func find(ctx context.Context, run Runner, name string) (Container, error) {
	if !ValidName(name) {
		return Container{}, fmt.Errorf("invalid container name %q", name)
	}
	all, err := list(ctx, run, false)
	if err != nil {
		return Container{}, err
	}
	for _, c := range all {
		if c.Name == name {
			return c, nil
		}
	}
	return Container{}, fmt.Errorf("container %q not found", name)
}

// healStaleTask moves a dead containerd task directory named in a Docker
// "file exists" refusal aside. A task whose init process is alive is never
// touched.
func healStaleTask(output string) (string, bool) {
	m := staleRE.FindStringSubmatch(output)
	if m == nil {
		return "", false
	}
	dir := filepath.Join(TaskRoot, m[1])
	if pidRaw, err := os.ReadFile(filepath.Join(dir, "init.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw))); err == nil && pid > 0 && procAlive(pid) {
			return "", false
		}
	}
	if err := os.MkdirAll(StaleRoot, 0o700); err != nil {
		return "", false
	}
	dst := filepath.Join(StaleRoot, m[1]+"-"+time.Now().UTC().Format("20060102T150405"))
	if err := os.Rename(dir, dst); err != nil {
		return "", false
	}
	return dst, true
}

// Logs returns the last lines (≤ MaxLogLines, ≤ 64 KiB) without colour codes.
func Logs(ctx context.Context, run Runner, name string, lines int) (string, error) {
	if lines <= 0 || lines > MaxLogLines {
		lines = MaxLogLines
	}
	if _, err := find(ctx, run, name); err != nil {
		return "", err
	}
	raw, err := run(ctx, "logs", "--tail", strconv.Itoa(lines), "--timestamps", name)
	if err != nil && len(raw) == 0 {
		return "", fmt.Errorf("docker logs %s: %w", name, err)
	}
	text := ansiRE.ReplaceAllString(string(raw), "")
	if len(text) > maxLogBytes {
		text = text[len(text)-maxLogBytes:]
	}
	return text, nil
}

// Docker runs /usr/bin/docker for the root broker with a minimal environment
// and at most 1 MiB of combined output.
func Docker(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/docker", args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "HOME=/nonexistent"}
	out, err := cmd.CombinedOutput()
	if len(out) > 1<<20 {
		out = out[:1<<20]
	}
	return out, err
}

// Dispatch is the broker entry: args are ["list"], ["act", name, action] or
// ["logs", name, lines]. The answer is JSON.
func Dispatch(ctx context.Context, run Runner, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("missing containers operation")
	}
	var v any
	var err error
	switch {
	case args[0] == "list" && len(args) == 1:
		var list []Container
		list, err = List(ctx, run)
		v = map[string]any{"containers": list}
	case args[0] == "act" && len(args) == 3:
		v, err = Act(ctx, run, args[1], args[2])
	case args[0] == "logs" && len(args) == 3:
		n, _ := strconv.Atoi(args[2])
		var text string
		text, err = Logs(ctx, run, args[1], n)
		v = map[string]string{"logs": text}
	default:
		return nil, fmt.Errorf("unsupported containers arguments: %q", args)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func bounded(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = s[:n]
	}
	return s
}
