package jobs

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/policy"
)

func TestResourceViolationEvaluation(t *testing.T) {
	j := &Job{
		MemoryLimitBytes:    1024,
		CPUTimeLimitSeconds: 2,
		ProcessLimit:        3,
	}
	cases := []struct {
		name string
		obs  ProcessObservation
		want string
	}{
		{name: "ok", obs: ProcessObservation{RSSBytes: 512, CPUTicks: 100, ProcessCount: 2}},
		{name: "memory", obs: ProcessObservation{RSSBytes: 2048, ProcessCount: 1}, want: "memory rss"},
		{name: "processes", obs: ProcessObservation{RSSBytes: 512, ProcessCount: 4}, want: "process count"},
		{name: "cpu", obs: ProcessObservation{RSSBytes: 512, ProcessCount: 1, CPUTicks: 201}, want: "cpu time ticks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourceViolation(j, tc.obs, 100)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("unexpected violation %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("violation=%q want substring %q", got, tc.want)
			}
		})
	}
}

func TestProcessLimitTerminatesJob(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	work := t.TempDir()
	// Isolated resource-limit fixture runs shell code intentionally. Normal
	// operator policy still rejects unsandboxed shell -c execution.
	p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{},
		AllowedCommandClasses: map[policy.CommandClass]bool{policy.ClassReadOnly: true, policy.ClassMutating: true, policy.ClassPrivileged: true}}
	m, err := New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	m.defaultMemoryLimit = MaxMemoryLimit
	m.defaultCPUTimeLimit = MaxCPUTimeLimit
	m.defaultProcessLimit = 1

	started, err := m.StartWithOptions(sh, []string{"-c", "sleep 10 & wait"}, work, StartOptions{
		Timeout:      10 * time.Second,
		ProcessLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, started.ID)
	if s.State != Failed {
		t.Fatalf("state=%s want failed: %+v", s.State, s)
	}
	if !strings.Contains(s.ResourceViolation, "process count") || !strings.Contains(s.Error, "resource limit") {
		t.Fatalf("resource violation not reported: %+v", s)
	}
}

func TestResourceDefaultsPersistInSnapshot(t *testing.T) {
	m, work := testManager(t)
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	started, err := m.Start(printf, []string{"ok"}, work)
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, started.ID)
	if s.MemoryLimitBytes != DefaultMemoryLimit ||
		s.CPUTimeLimitSeconds != int64(DefaultCPUTimeLimit/time.Second) ||
		s.ProcessLimit != DefaultProcessLimit {
		t.Fatalf("resource defaults missing: %+v", s)
	}
}
