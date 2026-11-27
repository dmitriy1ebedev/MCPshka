package jobs

import (
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestBuildScopedCommandUsesHardCgroupProperties(t *testing.T) {
	unit, cmd := buildScopedCommand("abc-1", "/usr/bin/printf", []string{"hello"}, 256*1024*1024, 150, 12)
	if unit != "mcpshka-job-abc-1.scope" {
		t.Fatalf("unit=%q", unit)
	}
	if cmd.Path != systemdRunPath {
		t.Fatalf("path=%q want=%q", cmd.Path, systemdRunPath)
	}
	for _, want := range []string{
		"--scope", "--property=Slice=mcpshka-heavy.slice",
		"--collect",
		"--unit=mcpshka-job-abc-1.scope",
		"--property=MemoryMax=268435456",
		"--property=CPUQuota=150%",
		"--property=TasksMax=64", "--property=CPUWeight=20", "--property=IOWeight=10",
		"/usr/bin/printf",
		"hello",
	} {
		if !slices.Contains(cmd.Args, want) {
			t.Fatalf("missing arg %q in %#v", want, cmd.Args)
		}
	}
}

func TestScopeUnitValidation(t *testing.T) {
	for _, unit := range []string{
		"mcpshka-job-abc-1.scope",
		"mcpshka-job-123.scope",
	} {
		if !validScopeUnit(unit) {
			t.Fatalf("valid unit rejected: %q", unit)
		}
	}
	for _, unit := range []string{
		"",
		"ssh.service",
		"mcpshka-job-../../ssh.scope",
		"mcpshka-job-ABC.scope",
	} {
		if validScopeUnit(unit) {
			t.Fatalf("invalid unit accepted: %q", unit)
		}
	}
}

func TestScopeDisappearanceFailsClosed(t *testing.T) {
	unit := "mcpshka-job-scope-disappearance-test.scope"
	for _, raw := range []string{"LoadState=loaded\nActiveState=active\nControlGroup=/system.slice/" + unit, "LoadState=loaded\nActiveState=inactive\nControlGroup=/system.slice/other.scope", "LoadState=loaded\nActiveState=inactive", "LoadState=not-found\nActiveState=inactive\nControlGroup=/system.slice/" + unit, "LoadState=loaded\nActiveState=inactive\nControlGroup=\nActiveState=inactive"} {
		if gone, err := scopeStateGone(unit, raw); gone || err == nil {
			t.Fatalf("unsafe scope accepted: %q %v", raw, err)
		}
	}
	if gone, err := scopeStateGone(unit, "LoadState=not-found\nActiveState=inactive\nControlGroup="); !gone || err != nil {
		t.Fatalf("collected scope rejected: %v %v", gone, err)
	}
}

func TestSystemdScopeIntegration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("systemd scope integration requires root")
	}
	m, work := testManager(t)
	if err := m.EnableSystemdScopes(); err != nil {
		t.Skipf("systemd scopes unavailable: %v", err)
	}
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	started, err := m.StartWithOptions(printf, []string{"scope-ok"}, work, StartOptions{
		Timeout:          10 * time.Second,
		OutputLimitBytes: 4096,
		MemoryLimitBytes: 256 * 1024 * 1024,
		CPUQuotaPercent:  100,
		ProcessLimit:     8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.ScopeUnit == "" {
		t.Fatalf("missing scope unit: %+v", started)
	}
	finished := waitForTerminal(t, m, started.ID)
	if finished.State != Done {
		stderr, _ := m.Read(started.ID, "stderr", 0, 4096)
		t.Fatalf("scoped job state=%s err=%q stderr=%q", finished.State, finished.Error, stderr.Text)
	}
	stdout, err := m.Read(started.ID, "stdout", 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.Text != "scope-ok" {
		t.Fatalf("scoped stdout=%q", stdout.Text)
	}
	if err := m.WithGoneOwner(work, started.ID, func(Snapshot) error { return nil }); err != nil {
		t.Fatalf("completed scope could not release: %v", err)
	}
}

func TestCPUQuotaAndTasksAreClampedBeforeScope(t *testing.T) {
	m := &Manager{
		defaultCPUTimeLimit: MaxCPUTimeLimit,
		defaultMemoryLimit:  MaxMemoryLimit,
		defaultProcessLimit: DefaultProcessLimit,
	}
	_ = m
	unit, cmd := buildScopedCommand("cap", "/bin/true", nil, MaxMemoryLimit, MaxCPUQuotaPercent, MaxProcessLimit)
	if !validScopeUnit(unit) {
		t.Fatalf("bad unit: %q", unit)
	}
	if !slices.Contains(cmd.Args, "--property=CPUQuota=400%") {
		t.Fatalf("CPU quota missing: %#v", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "--property=TasksMax=1024") {
		t.Fatalf("TasksMax missing: %#v", cmd.Args)
	}
}
