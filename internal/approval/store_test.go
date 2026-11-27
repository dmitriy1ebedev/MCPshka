package approval

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testPlanHash() string { return strings.Repeat("a", 64) }

func TestGrantConsumeIsBoundAndSingleUse(t *testing.T) {
	store := New(t.TempDir())
	req, err := store.Request("rm", []string{"-f", "target"}, "/root/repos/test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, granted, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if granted.State != StateGranted || token == "" {
		t.Fatalf("grant=%+v token=%q", granted, token)
	}
	if _, err := store.Validate(token, "rm", []string{"-f", "other"}, "/root/repos/test"); err == nil {
		t.Fatal("mismatched args accepted")
	}
	consumed, err := store.Consume(token, "rm", []string{"-f", "target"}, "/root/repos/test")
	if err != nil {
		t.Fatal(err)
	}
	if consumed.State != StateConsumed {
		t.Fatalf("state=%s", consumed.State)
	}
	if _, err := store.Consume(token, "rm", []string{"-f", "target"}, "/root/repos/test"); err == nil {
		t.Fatal("token reused")
	}
}

func TestTokenIsNotPersisted(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	req, err := store.Request("rm", []string{"x"}, "/tmp", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, req.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || strings.Contains(string(data), parts[1]) {
		t.Fatal("plaintext approval secret persisted")
	}
}

func TestExpiredApprovalRejected(t *testing.T) {
	store := New(t.TempDir())
	req, err := store.Request("rm", []string{"x"}, "/tmp", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, _, err := store.Grant(req.ID); err == nil {
		t.Fatal("expired request granted")
	}
	status, err := store.Status(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateExpired {
		t.Fatalf("state=%s", status.State)
	}
}

func TestExpiredApprovalEntersHoldWithoutMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(root, "approvals"))
	req, err := store.RequestWithPlan("rm", []string{"-f", target}, root, testPlanHash(), time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	gate, err := store.Gate(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate.State != GateHold || gate.Allowed || gate.Severity != "warning" || gate.DedupKey != "approval:"+req.ID {
		t.Fatalf("unexpected gate: %+v", gate)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "unchanged" {
		t.Fatalf("target changed while approval held: data=%q err=%v", data, err)
	}
}

func TestApprovalGateStates(t *testing.T) {
	store := New(t.TempDir())
	req, err := store.RequestWithPlan("rm", []string{"x"}, "/tmp", testPlanHash(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := store.Gate(req.ID)
	if err != nil || gate.State != GateWaitingApproval || gate.Allowed {
		t.Fatalf("pending gate=%+v err=%v", gate, err)
	}
	token, _, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate, err = store.Gate(req.ID)
	if err != nil || gate.State != GateReady || !gate.Allowed {
		t.Fatalf("granted gate=%+v err=%v", gate, err)
	}
	if _, err := store.ConsumeWithPlan(token, "rm", []string{"x"}, "/tmp", testPlanHash()); err != nil {
		t.Fatal(err)
	}
	gate, err = store.Gate(req.ID)
	if err != nil || gate.State != GateCompleted || gate.Allowed {
		t.Fatalf("consumed gate=%+v err=%v", gate, err)
	}
}

func TestApprovalIsBoundToTrustedPlanHash(t *testing.T) {
	store := New(t.TempDir())
	planA := strings.Repeat("a", 64)
	planB := strings.Repeat("b", 64)
	req, err := store.RequestWithPlan("rm", []string{"x"}, "/tmp", planA, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if req.PlanHash != planA {
		t.Fatalf("plan_hash=%q", req.PlanHash)
	}
	token, _, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateWithPlan(token, "rm", []string{"x"}, "/tmp", planB); err == nil {
		t.Fatal("approval accepted a different plan hash")
	}
	if _, err := store.ConsumeWithPlan(token, "rm", []string{"x"}, "/tmp", planA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestWithPlan("rm", []string{"x"}, "/tmp", "not-a-hash", time.Minute); err == nil {
		t.Fatal("invalid plan hash accepted")
	}
}

func TestConcurrentConsumeOnlyOnce(t *testing.T) {
	store := New(t.TempDir())
	req, err := store.Request("rm", []string{"x"}, "/tmp", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ok int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Consume(token, "rm", []string{"x"}, "/tmp"); err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("successful consumes=%d want=1", ok)
	}
}
