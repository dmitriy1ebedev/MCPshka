package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/dockerapi"
	"mcpshka/internal/dockerlogs"
)

const (
	dockerPath         = "/usr/bin/docker"
	commandTimeout     = 20 * time.Second
	DefaultListEntries = 64
	MaxListEntries     = 128
	DefaultLogLines    = 100
	MaxLogLines        = 500
	DefaultLogBytes    = 32 * 1024
	MaxLogBytes        = dockerlogs.MaxBytes
	DefaultLogSince    = 15 * time.Minute
	MaxLogSince        = 24 * time.Hour
	MaxCommandBytes    = 64 * 1024
)

type ContainerSummary struct {
	Name   string `json:"name"`
	Image  string `json:"image,omitempty"`
	State  string `json:"state,omitempty"`
	Status string `json:"status,omitempty"`
}

type ListResult struct {
	Containers []ContainerSummary `json:"containers"`
	Warnings   []string           `json:"warnings,omitempty"`
	Truncated  bool               `json:"truncated"`
}

type Status struct {
	Name         string `json:"name"`
	Status       string `json:"status,omitempty"`
	Running      bool   `json:"running"`
	Restarting   bool   `json:"restarting"`
	OOMKilled    bool   `json:"oom_killed"`
	Dead         bool   `json:"dead"`
	PID          int    `json:"pid,omitempty"`
	ExitCode     int    `json:"exit_code,omitempty"`
	Error        string `json:"error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	HealthStatus string `json:"health_status,omitempty"`
}

type Logs = dockerlogs.Result

type Inspect struct {
	Name           string   `json:"name"`
	ID             string   `json:"id,omitempty"`
	Image          string   `json:"image,omitempty"`
	RestartPolicy  string   `json:"restart_policy,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
	ComposeService string   `json:"compose_service,omitempty"`
	Networks       []string `json:"networks,omitempty"`
	// Detached: a network entry has no endpoint. Meaningful only while the
	// container runs: it then has no IP or ports (torrserver 10.11).
	Detached bool     `json:"detached,omitempty"`
	Mounts   []string `json:"mounts,omitempty"`
	// Internal-only exact host sources for checking live bind-mount conflicts.
	// Never expose paths outside an allowed filesystem root to MCP callers.
	MountSources []string `json:"-"`
}

type Resources struct {
	Name        string `json:"name"`
	CPUPercent  string `json:"cpu_percent,omitempty"`
	MemoryUsage string `json:"memory_usage,omitempty"`
	MemoryPct   string `json:"memory_percent,omitempty"`
	NetIO       string `json:"net_io,omitempty"`
	BlockIO     string `json:"block_io,omitempty"`
	PIDs        string `json:"pids,omitempty"`
}

type runner func(context.Context, int, ...string) ([]byte, bool, error)

type Service struct {
	restartAllowed  map[string]struct{}
	mutationAllowed map[string]struct{}
	run             runner
	mutationMu      sync.Mutex
	brokered        bool
}

func NewFromEnvironment() *Service {
	restart := parseContainerList(os.Getenv("MCPSHKA_DOCKER_RESTART_CONTAINERS"))
	mutation := parseContainerList(os.Getenv("MCPSHKA_DOCKER_MUTATION_CONTAINERS"))
	svc := newService(restart, mutation, runDocker)
	if brokerclient.Enabled() {
		svc.run = runBrokerDocker
		svc.brokered = true
	}
	return svc
}

func newService(restartAllowed, mutationAllowed map[string]struct{}, run runner) *Service {
	if restartAllowed == nil {
		restartAllowed = map[string]struct{}{}
	}
	if mutationAllowed == nil {
		mutationAllowed = map[string]struct{}{}
	}
	return &Service{
		restartAllowed:  cloneSet(restartAllowed),
		mutationAllowed: cloneSet(mutationAllowed),
		run:             run,
	}
}

func parseContainerList(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if validContainerName(item) {
			out[item] = struct{}{}
		}
	}
	return out
}

func cloneSet(src map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(src))
	for name := range src {
		out[name] = struct{}{}
	}
	return out
}

func (s *Service) List(ctx context.Context, maxEntries int) (ListResult, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultListEntries
	}
	if maxEntries > MaxListEntries {
		maxEntries = MaxListEntries
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, outputTruncated, err := s.run(child, MaxCommandBytes,
		"ps", "-a", "--no-trunc", "--format", "{{json .}}",
	)
	if err != nil {
		return ListResult{}, fmt.Errorf("docker ps: %w: %s", err, bounded(raw))
	}
	result := ListResult{Truncated: outputTruncated}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// The broker combines stdout and stderr. Docker CLI warnings can
		// precede JSON rows; retain them as bounded diagnostic evidence.
		// Any other non-JSON line is still a hard parse error.
		if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "WARNING ") || strings.HasPrefix(line, "WARN[") {
			if len(result.Warnings) >= 8 {
				result.Truncated = true
				continue
			}
			if len(line) > 256 {
				line = line[:256]
				result.Truncated = true
			}
			result.Warnings = append(result.Warnings, line)
			continue
		}
		if len(result.Containers) >= maxEntries {
			result.Truncated = true
			break
		}
		var row struct {
			Names  string
			Image  string
			State  string
			Status string
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return ListResult{}, fmt.Errorf("decode docker ps row: %w", err)
		}
		if !validContainerName(row.Names) {
			continue
		}
		result.Containers = append(result.Containers, ContainerSummary{
			Name: row.Names, Image: row.Image, State: row.State, Status: row.Status,
		})
	}
	// Combined stdout/stderr containing only warnings is not a verifiable
	// inventory. Never report zero containers when Docker returned no rows.
	if len(result.Warnings) > 0 && len(result.Containers) == 0 {
		return ListResult{}, errors.New("docker warnings without verifiable container inventory")
	}
	return result, nil
}

func (s *Service) Status(ctx context.Context, name string) (Status, error) {
	if !validContainerName(name) {
		return Status{}, fmt.Errorf("invalid container name %q", name)
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, truncated, err := s.run(child, MaxCommandBytes,
		"inspect", "--format", "{{json .State}}", name,
	)
	if err != nil {
		return Status{}, fmt.Errorf("docker inspect %s: %w: %s", name, err, bounded(raw))
	}
	if truncated {
		return Status{}, errors.New("docker inspect state exceeded bounded output")
	}
	var state struct {
		Status     string
		Running    bool
		Restarting bool
		OOMKilled  bool
		Dead       bool
		Pid        int
		ExitCode   int
		Error      string
		StartedAt  string
		FinishedAt string
		Health     *struct {
			Status string
		}
	}
	// The narrow broker combines Docker stdout/stderr. Accept bounded CLI
	// diagnostics, but require exactly one verifiable JSON state object.
	var stateJSON string
	warnings := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "WARN[") {
			warnings++
			if warnings > 8 || len(line) > 256 {
				return Status{}, errors.New("docker inspect warnings exceed limits")
			}
			continue
		}
		if stateJSON != "" {
			return Status{}, errors.New("docker inspect returned multiple state rows")
		}
		stateJSON = line
	}
	if stateJSON == "" {
		return Status{}, errors.New("docker inspect did not return a state row")
	}
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return Status{}, fmt.Errorf("decode docker state: %w", err)
	}
	if state.Status == "" {
		return Status{}, errors.New("docker inspect state missing status")
	}
	out := Status{
		Name: name, Status: state.Status, Running: state.Running, Restarting: state.Restarting,
		OOMKilled: state.OOMKilled, Dead: state.Dead, PID: state.Pid, ExitCode: state.ExitCode,
		Error: state.Error, StartedAt: state.StartedAt, FinishedAt: state.FinishedAt,
	}
	if state.Health != nil {
		out.HealthStatus = state.Health.Status
	}
	return out, nil
}

func (s *Service) Inspect(ctx context.Context, name string) (Inspect, error) {
	if !validContainerName(name) {
		return Inspect{}, fmt.Errorf("invalid container name %q", name)
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, truncated, err := s.run(child, MaxCommandBytes,
		"inspect", "--format",
		"{{json .Id}}\n{{json .Config.Image}}\n{{json .HostConfig.RestartPolicy.Name}}\n{{json .Config.Labels}}\n{{json .NetworkSettings.Networks}}\n{{json .Mounts}}",
		name,
	)
	if err != nil {
		return Inspect{}, fmt.Errorf("docker inspect %s: %w: %s", name, err, bounded(raw))
	}
	if truncated {
		return Inspect{}, errors.New("docker inspect metadata exceeded bounded output")
	}
	// The broker combines stdout and stderr. Docker may emit CLI warnings
	// before, between or after the six JSON metadata rows. Ignore only
	// bounded warning lines; unexpected text and extra rows fail closed.
	lines := make([]string, 0, 6)
	warnings := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "WARN[") {
			warnings++
			if warnings > 8 || len(line) > 256 {
				return Inspect{}, errors.New("docker inspect metadata warnings exceed limits")
			}
			continue
		}
		if len(lines) >= 6 {
			return Inspect{}, errors.New("docker inspect metadata has extra rows")
		}
		lines = append(lines, line)
	}
	if len(lines) != 6 {
		return Inspect{}, errors.New("docker inspect metadata requires exactly six rows")
	}
	out := Inspect{Name: name}
	if err := json.Unmarshal([]byte(lines[0]), &out.ID); err != nil {
		return Inspect{}, fmt.Errorf("decode docker id: %w", err)
	}
	if len(out.ID) > 12 {
		out.ID = out.ID[:12]
	}
	if err := json.Unmarshal([]byte(lines[1]), &out.Image); err != nil {
		return Inspect{}, fmt.Errorf("decode docker image: %w", err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &out.RestartPolicy); err != nil {
		return Inspect{}, fmt.Errorf("decode docker restart policy: %w", err)
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(lines[3]), &labels); err != nil {
		return Inspect{}, fmt.Errorf("decode docker labels: %w", err)
	}
	out.ComposeProject = labels["com.docker.compose.project"]
	out.ComposeService = labels["com.docker.compose.service"]
	var networks map[string]struct{ EndpointID string }
	if err := json.Unmarshal([]byte(lines[4]), &networks); err != nil {
		return Inspect{}, fmt.Errorf("decode docker networks: %w", err)
	}
	for network, n := range networks {
		if len(out.Networks) >= 32 {
			break
		}
		out.Networks = append(out.Networks, network)
		if n.EndpointID == "" && network != "host" && network != "none" {
			out.Detached = true
		}
	}
	sort.Strings(out.Networks)
	var mounts []struct {
		Type        string
		Source      string
		Destination string
	}
	if err := json.Unmarshal([]byte(lines[5]), &mounts); err != nil {
		return Inspect{}, fmt.Errorf("decode docker mounts: %w", err)
	}
	if len(mounts) > 32 {
		return Inspect{}, errors.New("docker mount list exceeded 32 entries; inventory is UNKNOWN")
	}
	for _, mount := range mounts {
		if mount.Destination == "" {
			continue
		}
		out.Mounts = append(out.Mounts, mount.Type+":"+mount.Destination)
		if mount.Source != "" {
			out.MountSources = append(out.MountSources, mount.Source)
		}
	}
	return out, nil
}

func (s *Service) Resources(ctx context.Context, name string) (Resources, error) {
	if !validContainerName(name) {
		return Resources{}, fmt.Errorf("invalid container name %q", name)
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, truncated, err := s.run(child, MaxCommandBytes,
		"stats", "--no-stream", "--format", "{{json .}}", name,
	)
	if err != nil {
		return Resources{}, fmt.Errorf("docker stats %s: %w: %s", name, err, bounded(raw))
	}
	if truncated {
		return Resources{}, errors.New("docker stats exceeded bounded output")
	}
	var row struct {
		Name     string
		CPUPerc  string
		MemUsage string
		MemPerc  string
		NetIO    string
		BlockIO  string
		PIDs     string
	}
	// The privileged Docker adapter merges CLI warnings and JSON output.
	// Require one unambiguous stats row, never accept warnings-only data.
	var statsJSON string
	warnings := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "WARN[") {
			warnings++
			if warnings > 8 || len(line) > 256 {
				return Resources{}, errors.New("docker stats warnings exceed limits")
			}
			continue
		}
		if statsJSON != "" {
			return Resources{}, errors.New("docker stats returned multiple data rows")
		}
		statsJSON = line
	}
	if statsJSON == "" {
		return Resources{}, errors.New("docker stats did not return a data row")
	}
	if err := json.Unmarshal([]byte(statsJSON), &row); err != nil {
		return Resources{}, fmt.Errorf("decode docker stats: %w", err)
	}
	if row.Name == "" {
		return Resources{}, errors.New("docker stats missing container identity")
	}
	if row.Name != "" && row.Name != name {
		return Resources{}, fmt.Errorf("docker stats returned unexpected container %q", row.Name)
	}
	return Resources{
		Name: name, CPUPercent: row.CPUPerc, MemoryUsage: row.MemUsage,
		MemoryPct: row.MemPerc, NetIO: row.NetIO, BlockIO: row.BlockIO, PIDs: row.PIDs,
	}, nil
}

func (s *Service) Action(ctx context.Context, name, action string) (Status, error) {
	if !validContainerName(name) {
		return Status{}, fmt.Errorf("invalid container name %q", name)
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start", "stop", "restart":
	default:
		return Status{}, fmt.Errorf("unsupported docker action %q", action)
	}
	_, mutationAllowed := s.mutationAllowed[name]
	_, restartAllowed := s.restartAllowed[name]
	// Starting a stopped container that may be restarted is no wider than a
	// restart (08.11: Zigbee2MQTT exited and docker_action start was refused).
	if !mutationAllowed && !((action == "restart" || action == "start") && restartAllowed) {
		return Status{}, fmt.Errorf("container %q is not in the mutation allowlist for %s", name, action)
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	args := []string{action}
	if action == "stop" || action == "restart" {
		args = append(args, "--time", "10")
	}
	args = append(args, name)
	raw, _, err := s.run(child, 4096, args...)
	if err != nil {
		return Status{}, fmt.Errorf("docker %s %s: %w: %s", action, name, err, bounded(raw))
	}
	return s.Status(ctx, name)
}

func (s *Service) Logs(ctx context.Context, name string, since time.Duration, lines, maxBytes int) (Logs, error) {
	return s.LogsPage(ctx, dockerlogs.Query{Container: name, SinceSeconds: int64(since / time.Second), Lines: lines, MaxBytes: maxBytes})
}

func (s *Service) LogsPage(ctx context.Context, query dockerlogs.Query) (Logs, error) {
	if s.brokered {
		// Broker validates independently and reduces the stream before serialization.
		raw, _ := json.Marshal(query)
		data, err := brokerclient.Run(ctx, "docker", "logs-page", string(raw))
		if err != nil {
			return Logs{}, fmt.Errorf("docker logs request failed: %w", err)
		}
		var result Logs
		if err := json.Unmarshal(data, &result); err != nil {
			return Logs{}, errors.New("invalid Docker log page response")
		}
		return result, nil
	}
	return dockerlogs.Read(ctx, query, dockerlogs.RunDocker)
}

func (s *Service) Restart(ctx context.Context, name string) (Status, error) {
	return s.Action(ctx, name, "restart")
}

func validContainerName(name string) bool {
	if len(name) < 1 || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if i > 0 && (r == '_' || r == '-' || r == '.') {
			continue
		}
		return false
	}
	return true
}

type cappedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		b.buf = append(b.buf, p[:keep]...)
	}
	if remaining <= 0 || len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func runDocker(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
	if len(args) == 2 && args[0] == "mdns" && args[1] == "go2rtc" {
		raw, _ := dockerapi.Inspect(ctx, args)
		return raw, false, nil
	}
	buf := &cappedBuffer{limit: maxBytes}
	cmd := exec.CommandContext(ctx, dockerPath, args...)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	return append([]byte(nil), buf.buf...), buf.truncated, err
}

func runBrokerDocker(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
	raw, err := brokerclient.Run(ctx, "docker", args...)
	out, truncated := brokerclient.Cap(raw, maxBytes)
	return out, truncated, err
}

func bounded(data []byte) string {
	if len(data) > 2048 {
		data = data[:2048]
	}
	return strings.TrimSpace(string(data))
}

// RestartAllowed reports whether a restart (docker_action) may act on name.
func (s *Service) RestartAllowed(name string) bool {
	_, ok := s.restartAllowed[name]
	return ok
}

// RestartContainers returns a stable copy of the exact restart allowlist.
func (s *Service) RestartContainers() []string {
	names := make([]string, 0, len(s.restartAllowed))
	for name := range s.restartAllowed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
