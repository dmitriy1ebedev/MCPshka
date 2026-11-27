package rootbroker

import (
	"strings"
	"testing"
	"time"
)

func TestRoadmapRunContract(t *testing.T) {
	ok := []string{"roadmap-run", strings.Repeat("a", 32), "owner", "/root/repos/diag", "5", "8", "agents:codex,claude"}
	if err := validateRoadmapRun(ok); err != nil {
		t.Fatal(err)
	}
	for i, v := range map[int]string{3: "/root/repos/mcpshka-controlled", 4: "0", 5: "25", 2: "local-stdio"} {
		bad := append([]string(nil), ok...)
		bad[i] = v
		if validateRoadmapRun(bad) == nil {
			t.Fatal("unsafe roadmap run accepted", bad)
		}
	}
	for _, path := range []string{"/root/repos", "/etc", "/root/repos/diag/sub", "/root/repos/../x"} {
		bad := append([]string(nil), ok...)
		bad[3] = path
		if validateRoadmapRun(bad) == nil {
			t.Fatal("path accepted", path)
		}
	}
	at := time.Date(2026, 10, 21, 23, 5, 7, 0, time.UTC)
	id := roadmapRunID("/root/repos/diag", at)
	if !strings.HasPrefix(id, "diag-20261021-230507-") || id == roadmapRunID("/root/repos/diag", at) {
		t.Fatal("run ids must be unique per start", id)
	}
	args := strings.Join(roadmapRunServiceArgs(ok, id), " ")
	for _, want := range []string{"--unit=mcpshka-roadmap-" + id, "RuntimeMaxSec=29400s", "Nice=10", "CPUQuota=150%", installedMCPshka + " roadmap-run " + id + " /root/repos/diag 5 8"} {
		if !strings.Contains(args, want) {
			t.Fatalf("unit lacks %q: %s", want, args)
		}
	}
}

func TestAgentTaskContract(t *testing.T) {
	ok := []string{"agent-task", strings.Repeat("a", 32), "owner", "0123456789abcdef", "claude", "/root/repos/diag"}
	if err := validateAgentTask(ok); err != nil {
		t.Fatal(err)
	}
	for i, v := range map[int]string{3: "../x", 4: "gpt", 5: "/root/repos/mcpshka-controlled", 2: "local-stdio"} {
		bad := append([]string(nil), ok...)
		bad[i] = v
		if validateAgentTask(bad) == nil {
			t.Fatal("unsafe agent task accepted", bad)
		}
	}
	args := strings.Join(agentTaskServiceArgs(ok), " ")
	if !strings.Contains(args, installedMCPshka+" agent-task 0123456789abcdef claude /root/repos/diag") || !strings.Contains(args, "RuntimeMaxSec=32400s") {
		t.Fatal(args)
	}
	for _, bound := range []string{"--unit=mcpshka-agent-task-runner-0123456789abcdef", "--setenv=MCPSHKA_AGENT_RUNNER=0123456789abcdef", "--property=MemoryHigh=128M", "--property=CPUWeight=20", "--property=MemoryMax=256M", "--property=TasksMax=32", "--setenv=GOMAXPROCS=2", "--setenv=GOFLAGS=-p=1", "--setenv=GOMEMLIMIT=128MiB"} {
		if !strings.Contains(args, bound) {
			t.Fatalf("agent unit lacks %s: %s", bound, args)
		}
	}
}
