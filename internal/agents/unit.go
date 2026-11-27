package agents

import (
	"context"
	"errors"
	"mcpshka/internal/heavyqueue"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/unitbus"
)

var errAttemptUnknown = errors.New("attempt outcome UNKNOWN; reconcile its unit before any resume")

// RunInUnit gives each attempt its own lifetime evidence. The waiting runner
// cannot make a quota-ended attempt's lease look live to stale recovery.
func RunInUnit(ctx context.Context, agent, dir, prompt, report string) (string, error) {
	id := strings.TrimSuffix(filepath.Base(report), ".report.md")
	runner := os.Getenv("MCPSHKA_AGENT_RUNNER")
	cmd, err := Command(ctx, agent, dir, prompt, report)
	if err != nil {
		return "", err
	}
	args, err := attemptUnitArgs(id, runner, dir, cmd.Args)
	if err != nil {
		return "", err
	}
	// systemd requires an absolute executable, unlike os/exec's PATH lookup.
	executable, err := exec.LookPath(cmd.Args[0])
	if err != nil {
		return "", err
	}
	args[len(args)-len(cmd.Args)] = executable
	wrapped := exec.CommandContext(ctx, "/usr/bin/systemd-run", args...)
	wrapped.WaitDelay = 5 * time.Second // reach cleanup even if an attempt still holds the output pipe
	tail, err := runCommand(ctx, wrapped, agent, report)
	if ctx.Err() != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := exec.CommandContext(cleanup, "/usr/bin/systemctl", "stop", "mcpshka-agent-task-"+id+".service").Run(); stopErr != nil {
			return tail, errAttemptUnknown
		}
	}
	// A lost systemd-run response is not proof that its agent stopped.
	if ended, probeErr := attemptEnded(context.Background(), id); probeErr != nil || !ended {
		return tail, errAttemptUnknown
	}
	return tail, err
}

func attemptEnded(ctx context.Context, id string) (bool, error) {
	if !ValidTaskID(id) {
		return false, errors.New("invalid task id")
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := unitbus.Output(probe, "show", "--property=ActiveState", "--value", "mcpshka-agent-task-"+id+".service")
	state := strings.TrimSpace(string(raw))
	return err == nil && (state == "inactive" || state == "failed"), err
}

func attemptUnitArgs(id, runner, dir string, command []string) ([]string, error) {
	if !ValidTaskID(id) || !ValidTaskID(runner) || len(command) == 0 {
		return nil, errors.New("invalid task attempt identity")
	}
	lim := attemptLimit(command[0])
	return heavyqueue.HeavyUnitArgs(append([]string{"--unit=mcpshka-agent-task-" + id, "--collect", "--wait", "--pipe", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root",
		"--property=BindsTo=mcpshka-agent-task-runner-" + runner + ".service",
		"--property=RuntimeMaxSec=10800s", "--property=KillMode=control-group",
		"--property=MemoryMax=" + lim.max, "--property=MemoryHigh=" + lim.high, "--property=MemorySwapMax=" + lim.swap, "--property=TasksMax=512",
		"--property=CPUQuota=150%", "--working-directory=" + dir,
		"--setenv=HOME=/root", "--setenv=PATH=/root/.local/bin:/usr/local/bin:/usr/bin:/bin",
		"--setenv=GOMAXPROCS=2", "--setenv=GOFLAGS=-p=1", "--setenv=GOMEMLIMIT=768MiB", "--"}, command...)), nil
}

// P31.6: each agent CLI gets its own memory envelope on the 3.7 GB H96, so
// one agent cannot push HA/Zigbee into swap. The CLIs are light (Codex ~130
// MB, Claude Code ~470 MB RSS); builds and tests they start run as MCPshka
// jobs under the heavy queue, not inside this unit.
type unitLimit struct{ high, max, swap string }

var attemptLimits = map[string]unitLimit{
	"codex":  {high: "512M", max: "1G", swap: "256M"},
	"claude": {high: "1G", max: "1536M", swap: "256M"},
}

func attemptLimit(binary string) unitLimit {
	if l, ok := attemptLimits[filepath.Base(binary)]; ok {
		return l
	}
	return unitLimit{high: "1G", max: "1536M", swap: "256M"}
}
