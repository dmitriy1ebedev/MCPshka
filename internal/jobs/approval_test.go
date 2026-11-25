package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/approval"
	"mcpshka/internal/policy"
)

func TestDestructiveApprovalIsBoundAndConsumed(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "remove-me")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	p := policy.Default()
	p.AllowedWorkdirs = []string{work}
	m, err := New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	store := approval.New(t.TempDir())
	m.SetApprovalStore(store)

	rmdir, err := exec.LookPath("rmdir")
	if err != nil {
		t.Skip("rmdir not found")
	}
	if _, err := m.Start(rmdir, []string{target}, work); err == nil {
		t.Fatal("destructive command started without approval")
	}
	planHash := strings.Repeat("a", 64)
	req, err := store.RequestWithPlan(rmdir, []string{target}, work, planHash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWithOptions(rmdir, []string{target}, work, StartOptions{ApprovalToken: token, PlanHash: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("destructive approval accepted a different plan hash")
	}
	started, err := m.StartWithOptions(rmdir, []string{target}, work, StartOptions{ApprovalToken: token, PlanHash: planHash})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitForTerminal(t, m, started.ID); s.State != Done {
		t.Fatalf("approved destructive job failed: %+v", s)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if _, err := m.StartWithOptions(rmdir, []string{target}, work, StartOptions{ApprovalToken: token, PlanHash: planHash}); err == nil {
		t.Fatal("consumed approval token reused")
	}
}

func TestExplicitDenylistCannotBeApproved(t *testing.T) {
	work := t.TempDir()
	p := policy.Default()
	p.AllowedWorkdirs = []string{work}
	m, err := New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	store := approval.New(t.TempDir())
	m.SetApprovalStore(store)
	req, err := store.Request("reboot", nil, work, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWithOptions("reboot", nil, work, StartOptions{ApprovalToken: token}); err == nil {
		t.Fatal("explicit denylist bypassed by approval")
	}
}
