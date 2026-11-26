package rootbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/agentlogin"
	"mcpshka/internal/containerctl"
	"mcpshka/internal/diagcontract"
	"mcpshka/internal/diagobserver"
	"mcpshka/internal/dockerapi"
	"mcpshka/internal/dockersmoke"
	"mcpshka/internal/dockerterminal"
	"mcpshka/internal/hostclean"
	"mcpshka/internal/hostconfig"
	"mcpshka/internal/hostprocess"
	"mcpshka/internal/rootbroker/diagtraffic"
	"mcpshka/internal/rootledgerreap"
	"mcpshka/internal/tools/lanetools"
	"mcpshka/internal/transientunit"
	"mcpshka/internal/unitbus"
	"mcpshka/internal/wifiscan"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	DefaultConfigPath = "/etc/mcpshka/broker.json"
	maxOutputBytes    = 64 * 1024
	commandTimeout    = 30 * time.Second
)

type Config struct {
	SystemdStatus   []string `json:"systemd_status"`
	SystemdRestart  []string `json:"systemd_restart"`
	SystemdMutation []string `json:"systemd_mutation"`
	DockerRead      []string `json:"docker_read"`
	DockerRestart   []string `json:"docker_restart"`
	DockerMutation  []string `json:"docker_mutation"`
	DockerTerminal  []string `json:"docker_terminal"`
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = DefaultConfigPath
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, errors.New("broker config must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return Config{}, fmt.Errorf("broker config permissions too broad: %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	systemdNames := append(append(append([]string{}, cfg.SystemdStatus...), cfg.SystemdRestart...), cfg.SystemdMutation...)
	for _, name := range systemdNames {
		if !validUnit(name) {
			return Config{}, fmt.Errorf("invalid systemd unit %q", name)
		}
	}
	dockerNames := append(append(append([]string{}, cfg.DockerRead...), cfg.DockerRestart...), cfg.DockerMutation...)
	dockerNames = append(dockerNames, cfg.DockerTerminal...)
	for _, name := range dockerNames {
		if !validContainer(name) {
			return Config{}, fmt.Errorf("invalid docker container %q", name)
		}
	}
	return cfg, nil
}

func Run(ctx context.Context, cfg Config, family string, args []string) ([]byte, error) {
	if family == "docker" && len(args) > 0 && args[0] == "logs-page" {
		return runDockerLogsPage(ctx, cfg, args)
	}

	if family == "process-signal" {
		return hostprocess.RunSignal(ctx, args)
	}
	if family == "storage" {
		return runStorage(ctx, args)
	}
	if family == "health" {
		return runHealth(ctx, args)
	}
	if family == "release-apply-status" {
		return runReleaseApplyStatus(args)
	}
	if family == "release-proof" {
		return runReleaseProof(ctx, args)
	}
	if family == "historical-fence" {
		return runHistoricalFenceProof(ctx, args)
	}
	if family == "admin-session" {
		return RunAdminSession(ctx, args)
	}
	if family == "root-task" {
		return RunRootTask(ctx, args)
	}
	if family == "passkey" {
		return RunPasskey(ctx, args)
	}
	if family == "containers" {
		return containerctl.Dispatch(ctx, containerctl.Docker, args)
	}
	if family == "cleanup" {
		return hostclean.Dispatch(ctx, hostclean.Exec, args)
	}
	if family == "docker-terminal" {
		return dockerterminal.Run(ctx, cfg.DockerTerminal, args)
	}
	if family == "executor" {
		return runExecutor(ctx, args)
	}
	if family == "rootledger-reap" {
		return rootledgerreap.Run(ctx, args)
	}
	switch family {
	case "wifi-scan":
		return wifiscan.Run(ctx, args)
	case "diag-observer":
		return diagobserver.Run(ctx, args)
	case "docker-smoke":
		return dockersmoke.Run(ctx, args)
	case "ha-config":
		return runHAConfig(args)
	case "smb-share-status":
		return hostconfig.RunSambaStatus(args)
	case "approved-task":
		return runApprovedTask(ctx, args)
	case "agent-login":
		return agentlogin.Run(ctx, args)
	}
	if family == "executor-self-heal" {
		return runExecutorSelfHeal(ctx, args)
	}
	var bin string
	switch family {
	case "lane":
		return lanetools.Read(ctx, args)
	case "systemctl":
		if err := validateSystemctl(cfg, args); err != nil {
			return nil, err
		}
		// service_status/service_list every few seconds: a show goes over D-Bus.
		if out, ok := unitbus.Show(ctx, args...); ok && len(out) <= maxOutputBytes {
			return out, nil
		}
		bin = "/usr/bin/systemctl"
	case "journalctl":
		if err := validateJournalctl(cfg, args); err != nil {
			return nil, err
		}
		bin = "/usr/bin/journalctl"
	case "docker":
		if err := validateDocker(cfg, args); err != nil {
			return nil, err
		}
		if raw, ok := dockerapi.PS(ctx, args); ok {
			return filterDockerStatus(cfg.DockerRead, raw)
		} else if raw, ok = dockerapi.Inspect(ctx, args); ok {
			return raw, nil
		}
		bin = "/usr/bin/docker"
	case "systemd-run":
		if err := transientunit.ValidateRun(args); err != nil {
			return nil, err
		}
		bin = "/usr/bin/systemd-run"
	case "codex":
		if err := validateCodexBridge(args); err != nil {
			return nil, err
		}
		if args[0] == "task_submit" {
			// Serialize native task admission with owner-bound session load/resume.
			lock, err := openMutationLock(filepath.Join(taskRoot, "codex-admission.lock"))
			if err != nil {
				return nil, err
			}
			defer lock.Close()
		}
		bin = codexBridgePath()
		if err := validateRootOwnedExecutable(bin); err != nil {
			return nil, err
		}
	case "release-probe":
		if err := validateReleaseProbe(args); err != nil {
			return nil, err
		}
		bin = "/usr/local/libexec/mcpshka-release-probe.py"
		if err := validateRootOwnedExecutable(bin); err != nil {
			return nil, err
		}
	case "rescue":
		if err := validateRescue(args); err != nil {
			return nil, err
		}
		bin = "/usr/local/libexec/mcpshka-rescue.py"
		if err := validateRootOwnedExecutable(bin); err != nil {
			return nil, err
		}
	case "state-backup":
		if err := validateStateBackup(args); err != nil {
			return nil, err
		}
		bin, _ = os.Executable() // S5.GO: the broker's own Go code (statebackup)
	case "diag-traffic":
		if err := diagtraffic.Validate(args); err != nil {
			return nil, err
		}
		bin = "/proc/self/exe"
		args = append([]string{"diag-traffic"}, args...)
	case "diag-smoke":
		if err := diagcontract.ValidateSmoke(args); err != nil {
			return nil, err
		}
		if err := validateRootOwnedExecutable(diagcontract.SmokeHelper); err != nil {
			return nil, fmt.Errorf("diag smoke helper not installed: %w", err)
		}
		bin = diagcontract.SmokeHelper
	case "diag-release":
		if err := diagcontract.ValidateRelease(args); err != nil {
			return nil, err
		}
		if err := validateRootOwnedExecutable(diagcontract.ReleaseHelper); err != nil {
			return nil, fmt.Errorf("diag release helper not installed: %w", err)
		}
		bin = diagcontract.ReleaseHelper
	default:
		return nil, fmt.Errorf("unsupported broker family %q", family)
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(child, bin, args...)
	if family == "codex" || family == "state-backup" || family == "rescue" || family == "release-probe" {
		// Keep the broker's ProtectHome/PrivateTmp/read-only filesystem intact.
		// Only pinned short-lived capability clients run in bounded transient units.
		digest := sha256.Sum256([]byte(family + "\x00" + strings.Join(args, "\x00")))
		unit := fmt.Sprintf("mcpshka-cap-%x", digest[:12])
		if family == "release-probe" {
			// A retry after a client timeout collided with the still running
			// probe of the same arguments (BUG-20261023-68).
			nonce := make([]byte, 4)
			_, _ = rand.Read(nonce)
			unit += fmt.Sprintf("-%x", nonce)
		}
		argv := []string{"--quiet", "--collect", "--wait", "--pipe", "--unit=" + unit, "--property=Type=exec", "--property=RuntimeMaxSec=30s", "--property=TimeoutStartSec=30s", "--property=MemoryMax=256M", "--property=CPUQuota=50%", "--property=TasksMax=32", "--property=UMask=0077", "--property=NoNewPrivileges=yes", "/usr/bin/python3", bin, args[0]}
		if family == "rescue" {
			argv = append(argv, args[1:]...)
		}
		if family == "state-backup" {
			argv = append(argv[:len(argv)-3], bin, "state-backup", args[0], args[1])
		}
		if family == "release-probe" {
			argv = append(argv[:len(argv)-3], "--property=CapabilityBoundingSet=CAP_DAC_READ_SEARCH", "--property=ProtectSystem=strict", "--property=ProtectHome=read-only", "--property=ReadWritePaths=/usr/local/bin", "/usr/bin/python3", "-I", "-S", bin, args[0], args[1])
		}
		cmd = exec.CommandContext(child, "/usr/bin/systemd-run", argv...)
		if family == "codex" {
			cmd.Stdin = strings.NewReader(args[1])
		}
	}
	if family == "diag-smoke" || family == "diag-release" {
		// Only a root-owned, pinned helper may SCHEDULE a durable DIAG job.
		// No arbitrary executable, flags, Docker socket, shell or OAuth path
		// can be supplied by an MCP client. The helper must return quickly.
		cmd = exec.CommandContext(child, "/usr/bin/systemd-run", diagcontract.BoundedExecArgs(family, bin, args)...)
	}
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/nonexistent",
	}
	var out limitedBuffer
	out.limit = maxOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if family == "docker" && args[0] == "ps" {
		if child.Err() != nil {
			return nil, child.Err()
		}
		if out.truncated {
			return nil, errors.New("broker output exceeded limit")
		}
		if err != nil {
			return nil, err
		}
		return filterDockerStatus(cfg.DockerRead, out.Bytes())
	}
	if child.Err() != nil {
		return out.Bytes(), child.Err()
	}
	if out.truncated {
		return out.Bytes(), errors.New("broker output exceeded limit")
	}
	return out.Bytes(), err
}

var contains = slices.Contains[[]string, string]

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	if n > remaining {
		b.truncated = true
	}
	return n, nil
}

func (b *limitedBuffer) Bytes() []byte {
	return append([]byte(nil), b.buf.Bytes()...)
}

func ConfigPathFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("MCPSHKA_BROKER_CONFIG")); v != "" {
		return filepath.Clean(v)
	}
	return DefaultConfigPath
}

// Families in their own packages check their root-owned helpers this way.
func init() {
	dockerterminal.RootOwned = validateRootOwnedExecutable
	dockersmoke.RootOwned = validateRootOwnedExecutable
	agentlogin.RootOwnedTool = validateRootOwnedTool
	rootledgerreap.RootOwned = validateRootOwnedExecutable
}

func tail(s string, n int) string { return s[max(0, len(s)-n):] }
