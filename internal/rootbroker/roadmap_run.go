package rootbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/heavyqueue"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/repopath"
	"mcpshka/internal/roadmaprun"
)

// roadmap-run SESSION OWNER REPO MAX_PHASES HOURS starts the night runner for
// one repository under the owner's active Face ID window. The unit runs the
// installed mcpshka binary, which drives Codex phase by phase and asks
// MCPshka's phase_gate for every verdict; its journal is the morning report.
const installedMCPshka = "/usr/local/bin/mcpshka"

func validateRoadmapRun(args []string) error {
	if len(args) < 1 {
		return errors.New("explicit owner agent authorization required")
	}
	if _, err := roadmaprun.ParseAllowedAgents(args[len(args)-1]); err != nil {
		return err
	}
	args = args[:len(args)-1]
	if (len(args) != 6 && len(args) != 7) || args[0] != "roadmap-run" || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" ||
		!recipeRepository.MatchString(args[3]) || filepath.Clean(args[3]) != args[3] || args[3] == "/root/repos/mcpshka-controlled" {
		return errors.New("roadmap-run requires session, owner and an immediate repository")
	}
	phases, err1 := strconv.Atoi(args[4])
	hours, err2 := strconv.Atoi(args[5])
	if err1 != nil || err2 != nil || phases < 1 || phases > 62 || hours < 1 || hours > 24 || strconv.Itoa(phases) != args[4] || strconv.Itoa(hours) != args[5] {
		return errors.New("roadmap-run requires 1-62 phases and 1-24 hours")
	}
	if len(args) == 7 {
		if _, err := roadmaprun.DecodeGoal(args[6]); err != nil {
			return err
		}
	}
	return nil
}

// roadmapRunID names one run uniquely: repository, UTC start second and a
// random tail (a minute-only id collided with a manually scheduled run and
// overwrote its journal, BUG-20261022-45).
func roadmapRunID(repo string, now time.Time) string {
	tail := make([]byte, 2)
	_, _ = rand.Read(tail)
	return filepath.Base(repo) + "-" + now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(tail)
}

func roadmapRunServiceArgs(args []string, id string) []string {
	out := []string{"--unit=mcpshka-roadmap-" + id, "--collect", "--no-block", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root",
		"--property=RuntimeMaxSec=" + strconv.Itoa(mustAtoi(args[5])*3600+600) + "s",
		"--property=MemoryMax=3G", "--property=TasksMax=512", "--property=KillMode=control-group",
		// Bound memory and compiler concurrency alongside the shared scheduling class.
		"--property=MemoryHigh=2G", "--property=CPUQuota=150%",
		"--setenv=GOMAXPROCS=2", "--setenv=GOFLAGS=-p=1", "--setenv=GOMEMLIMIT=768MiB",
		"--setenv=MCPSHKA_AGENT_SESSION_ID=" + args[1], "--setenv=MCPSHKA_AGENT_OWNER=" + args[2],
		"--setenv=HOME=/root", "--setenv=PATH=/root/.local/bin:/usr/local/bin:/usr/bin:/bin",
		installedMCPshka, "roadmap-run", id, args[3], args[4], args[5]}
	out = append(out, args[6:]...)
	return heavyqueue.HeavyUnitArgs(out)
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

type RoadmapRunStart struct {
	ID    string `json:"id"`
	Unit  string `json:"unit"`
	State string `json:"state"`
}

func runRoadmapRun(ctx context.Context, args []string) ([]byte, error) {
	if err := validateRoadmapRun(args); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable(installedMCPshka); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	id := roadmapRunID(args[3], time.Now())
	unit := "mcpshka-roadmap-" + id
	if alive, err := administrativeWorkerAlive(ctx, unit+".service"); err != nil || alive {
		return nil, fmt.Errorf("roadmap run %s already active or unobservable: HOLD", id)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", roadmapRunServiceArgs(args, id)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil || stderr.truncated {
		return nil, fmt.Errorf("roadmap run start UNKNOWN; inspect unit %s: %.4096s", unit, stderr.Bytes())
	}
	return json.Marshal(RoadmapRunStart{ID: id, Unit: unit, State: "STARTED"})
}

// agent-task SESSION OWNER ID AGENT REPO runs one delegated prompt (written by
// the service to agents.TaskDir/<id>.prompt) with Codex or Claude Code, so
// either agent can hand work to the other through MCPshka. A worktree task is
// agent-task SESSION OWNER ID AGENT WORKTREE REPO: the broker cannot see
// /root/repos, so the server resolves REPO, consent is checked for it and the
// runner refuses unless the worktree really belongs to it.
func validateAgentTask(args []string) error {
	if (len(args) != 6 && len(args) != 7) || args[0] != "agent-task" || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" ||
		!agents.ValidTaskID(args[3]) || (args[4] != agents.Codex && args[4] != agents.Claude) {
		return errors.New("agent-task requires session, owner, task id, codex|claude and an immediate repository or its /root/repos/wt worktree")
	}
	repo := agentTaskRepo(args)
	if len(args) == 7 && !repopath.Shape(args[5], repo) {
		return errors.New("agent-task worktree must be /root/repos/wt/<name> of an immediate repository")
	}
	if !recipeRepository.MatchString(repo) || filepath.Clean(repo) != repo || repo == "/root/repos/mcpshka-controlled" {
		return errors.New("agent-task requires an immediate repository or its /root/repos/wt worktree")
	}
	return nil
}

// agentTaskRepo is the repository whose consent covers the task.
func agentTaskRepo(args []string) string { return args[len(args)-1] }

func agentTaskServiceArgs(args []string) []string {
	return heavyqueue.HeavyUnitArgs(append([]string{"--unit=mcpshka-agent-task-runner-" + args[3], "--collect", "--no-block", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root",
		"--property=RuntimeMaxSec=32400s",
		"--property=MemoryMax=256M", "--property=TasksMax=32", "--property=KillMode=control-group",
		// Only the observer lives here; each agent attempt has its own bounded unit.
		"--property=MemoryHigh=128M",
		"--setenv=GOMAXPROCS=2", "--setenv=GOFLAGS=-p=1", "--setenv=GOMEMLIMIT=128MiB",
		"--setenv=HOME=/root", "--setenv=PATH=/root/.local/bin:/usr/local/bin:/usr/bin:/bin",
		"--setenv=MCPSHKA_AGENT_RUNNER=" + args[3],
		"--setenv=MCPSHKA_AGENT_SESSION_ID=" + args[1], "--setenv=MCPSHKA_AGENT_OWNER=" + args[2],
		installedMCPshka, "agent-task", args[3], args[4]}, args[5:]...))
}

func runAgentTask(ctx context.Context, args []string) ([]byte, error) {
	if err := validateAgentTask(args); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable(installedMCPshka); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	unit := "mcpshka-agent-task-runner-" + args[3]
	if alive, err := administrativeWorkerAlive(ctx, unit+".service"); err != nil || alive {
		return nil, fmt.Errorf("agent task %s already active or unobservable: HOLD", args[3])
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", agentTaskServiceArgs(args)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil || stderr.truncated {
		return nil, fmt.Errorf("agent task start UNKNOWN; inspect unit %s: %.4096s", unit, stderr.Bytes())
	}
	return json.Marshal(RoadmapRunStart{ID: args[3], Unit: unit, State: "STARTED"})
}

// agent-task-cancel SESSION OWNER ID stops one delegated task under the
// owner's Face ID window; agents.Cancel records it only after both units end.
func runAgentTaskCancel(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 4 || !agents.ValidTaskID(args[3]) {
		return nil, errors.New("agent-task-cancel requires session, owner and task id")
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	lock.Close()
	if err := agents.StopUnits(ctx, args[3]); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"id": args[3], "units": "ended"})
}
