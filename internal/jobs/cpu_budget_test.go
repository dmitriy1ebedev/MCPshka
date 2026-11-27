package jobs

import (
	"os/exec"
	"testing"
	"time"
)

// DIAG ACTIVE-06 / MCP-122: an explicit two-hour wall budget carries a
// matching default CPU budget instead of the 30-minute default.
func TestExplicitWallTimeoutRaisesDefaultCPUBudget(t *testing.T) {
	m, work := testManager(t)
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	started, err := m.StartWithOptions(printf, []string{"ok"}, work, StartOptions{Timeout: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, started.ID)
	if s.CPUTimeLimitSeconds < int64((2*time.Hour)/time.Second) || s.CPUTimeLimitSeconds > int64(MaxCPUTimeLimit/time.Second) {
		t.Fatalf("cpu budget %ds for a 7200 s wall budget", s.CPUTimeLimitSeconds)
	}
	explicit, err := m.StartWithOptions(printf, []string{"ok"}, work, StartOptions{Timeout: 2 * time.Hour, CPUTimeLimit: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitForTerminal(t, m, explicit.ID); s.CPUTimeLimitSeconds > int64((2*time.Minute)/time.Second) {
		t.Fatalf("explicit cpu budget inflated to %ds", s.CPUTimeLimitSeconds)
	}
}
