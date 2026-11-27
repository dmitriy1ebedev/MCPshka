package projectlease

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestInteractiveLeaseDoesNotAdmitTokenlessNativeJobs(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	holder, err := s.AcquireTracked(root, "interactive-owner", strings.Repeat("d", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Guard(root, ""); err == nil {
		t.Fatal("unproven native writer admitted under interactive lease")
	}
	if _, err := s.ValidateHolder(root, holder.Token); err != nil {
		t.Fatal("guard invalidated legitimate holder token", err)
	}
	if _, err := s.Release(root, holder.Token); err != nil {
		t.Fatal(err)
	}
	if err := s.Guard(root, ""); err != nil {
		t.Fatal("released scope held", err)
	}
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("e", 32)
	if _, err := s.AcquireManaged(root, id, m); err != nil {
		t.Fatal("owned release did not allow managed handoff", err)
	}
	if err := s.Guard(root, "workflow-"+id); err != nil {
		t.Fatal("exact managed owner rejected", err)
	}
	if err := s.Guard(root, ""); err == nil {
		t.Fatal("foreign tokenless caller admitted")
	}
	if err := s.Guard(root, "workflow-"+strings.Repeat("f", 32)); err == nil {
		t.Fatal("different execution admitted")
	}
}

func TestTrackedInteractiveHolderMutationIsTokenBound(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	first, err := s.AcquireTracked(root, "operator-a", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	mutate := func() error { runs++; return nil }
	if err := s.WithInteractiveHolder(root, "", mutate); err == nil {
		t.Fatal("accepted missing token")
	}
	if err := s.WithInteractiveHolder(root, "wrong", mutate); err == nil {
		t.Fatal("accepted foreign token")
	}
	if runs != 0 {
		t.Fatal("foreign/missing owner executed effect")
	}
	if err := s.WithInteractiveHolder(root, first.Token, mutate); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatal("owner did not execute exactly once")
	}
	if err := s.Guard(root, ""); err == nil {
		t.Fatal("tokenless native job admitted")
	}
	if _, err := s.Release(root, first.Token); err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireTracked(root, "operator-b", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WithInteractiveHolder(root, first.Token, mutate); err == nil {
		t.Fatal("stale token admitted")
	}
	if runs != 1 {
		t.Fatal("stale holder executed effect")
	}
	if err := s.WithInteractiveHolder(root, second.Token, mutate); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatal("new holder failed")
	}
}

func TestInteractiveHolderRefusesUntrackedOrManagedLease(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	legacy, err := s.Acquire(root, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	effect := func() error { called = true; return nil }
	if err := s.WithInteractiveHolder(root, legacy.Token, effect); err == nil {
		t.Fatal("legacy holder admitted")
	}
	if called {
		t.Fatal("legacy effect executed")
	}
	if _, err := s.Release(root, legacy.Token); err != nil {
		t.Fatal(err)
	}
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := s.AcquireManaged(root, strings.Repeat("c", 32), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WithInteractiveHolder(root, owned.Token, effect); err == nil {
		t.Fatal("managed token admitted as interactive")
	}
	if called {
		t.Fatal("managed effect executed")
	}
}

// An independent Service uses a distinct file descriptor and SQLite handle.
// Releasing or reusing a lease must HOLD until the first effect has returned.
func TestInteractiveHolderFencesSeparateServices(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	holder, err := s.AcquireTracked(root, "writer", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	second := New(s.dir, s.policy)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.WithInteractiveHolder(root, holder.Token, func() error {
			close(entered)
			<-unblock
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("effect did not enter: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("effect did not acquire fence")
	}
	// An untrusted caller cannot force release while the effect is in flight.
	if _, err := second.Release(root, holder.Token); err == nil {
		close(unblock)
		t.Fatal("second service released an active holder")
	}
	if err := second.WithInteractiveHolder(root, holder.Token, func() error {
		t.Error("second service executed concurrently")
		return nil
	}); err == nil {
		close(unblock)
		t.Fatal("concurrent effect admitted")
	}
	close(unblock)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fenced operation did not finish")
	}
	if _, err := second.Release(root, holder.Token); err != nil {
		t.Fatalf("legitimate release refused after effect: %v", err)
	}
}

// Prove the lease fence also applies across independent processes; a mutex
// shared only by one in-memory Service would not pass this test.
func TestInteractiveFenceAcrossProcesses(t *testing.T) {
	if os.Getenv("MCPSHKA_FENCE_CHILD_TEST") == "1" {
		root := os.Getenv("MCPSHKA_FENCE_CHILD_ROOT")
		token := os.Getenv("MCPSHKA_FENCE_CHILD_TOKEN")
		p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
		child := New(filepath.Join(root, "leases"), p)
		if _, err := child.Release(root, token); err == nil {
			t.Fatal("child released active holder while parent effect runs")
		}
		if err := child.WithInteractiveHolder(root, token, func() error {
			t.Fatal("child concurrently entered effect")
			return nil
		}); err == nil {
			t.Fatal("child admitted while parent effect runs")
		}
		return
	}
	s, root := heartbeatSQLiteService(t)
	holder, err := s.AcquireTracked(root, "writer", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	unblock := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.WithInteractiveHolder(root, holder.Token, func() error {
			close(entered)
			<-unblock
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("effect not admitted: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("parent did not enter effect")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestInteractiveFenceAcrossProcesses$")
	cmd.Env = append(os.Environ(), "MCPSHKA_FENCE_CHILD_TEST=1", "MCPSHKA_FENCE_CHILD_ROOT="+root, "MCPSHKA_FENCE_CHILD_TOKEN="+holder.Token)
	output, childErr := cmd.CombinedOutput()
	close(unblock)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent effect remained stuck")
	}
	if childErr != nil {
		t.Fatalf("independent child failed: %v (%s)", childErr, output)
	}
	if _, err := s.Release(root, holder.Token); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveFenceRejectsSymlink(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	holder, err := s.AcquireTracked(root, "writer", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := s.leasePath(root) + ".effect.lock"
	// The acquired fixture now owns an existing fence; simulate path replacement.
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), lockPath); err != nil {
		t.Fatal(err)
	}
	executed := false
	if err := s.WithInteractiveHolder(root, holder.Token, func() error {
		executed = true
		return nil
	}); err == nil || executed {
		t.Fatal("followed untrusted lock symlink")
	}
	if _, err := s.Release(root, holder.Token); err == nil {
		t.Fatal("released holder without a trusted fence")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(root, holder.Token); err != nil {
		t.Fatal(err)
	}
}

func TestManagedGuardRejectsMalformedOwnerIdentity(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	m, err := jobs.New(filepath.Join(root, "jobs"), s.policy)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("e", 32)
	if _, err := s.AcquireManaged(root, id, m); err != nil {
		t.Fatal(err)
	}
	r, err := s.readRecord(s.leasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	r.Owner = "spoofed-display-owner"
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.database.Put("leases/"+filepath.Base(s.leasePath(root)), raw); err != nil {
		t.Fatal(err)
	}
	if err := s.Guard(root, "workflow-"+id); err == nil {
		t.Fatal("malformed display-owner binding admitted")
	}
}

func TestInteractiveAcquireRejectsUnusableFenceWithoutClaiming(t *testing.T) {
	for _, bad := range []string{"symlink", "shared", "permissions"} {
		t.Run(bad, func(t *testing.T) {
			s, root := heartbeatSQLiteService(t)
			if err := os.MkdirAll(s.dir, 0700); err != nil {
				t.Fatal(err)
			}
			lock := s.leasePath(root) + ".effect.lock"
			if bad == "symlink" {
				if err := os.Symlink(filepath.Join(root, "outside"), lock); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(lock, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if bad == "shared" {
					if err := os.Link(lock, filepath.Join(root, "alias")); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Chmod(lock, 0666); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AcquireTracked(root, "writer", strings.Repeat("a", 32)); err == nil {
				t.Fatal("issued a lease whose interactive fence cannot be used")
			}
			status, err := s.Status(root)
			if err != nil || status.Held {
				t.Fatalf("failed admission claimed a writer: %+v %v", status, err)
			}
		})
	}
}

func TestInteractiveHolderMayRunItsOwnJobs(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	first, err := s.AcquireTracked(root, "operator-a", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GuardHolder(root, ""); err == nil {
		t.Fatal("job admitted without a holder token")
	}
	if err := s.GuardHolder(root, "wrong"); err == nil {
		t.Fatal("foreign token admitted")
	}
	if err := s.GuardHolder(root, first.Token); err != nil {
		t.Fatal("current holder could not run its own job", err)
	}
	if err := s.Guard(root, ""); err == nil {
		t.Fatal("holder admission opened tokenless jobs")
	}
	if _, err := s.Release(root, first.Token); err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireTracked(root, "operator-b", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GuardHolder(root, first.Token); err == nil {
		t.Fatal("previous holder kept job admission")
	}
	if err := s.GuardHolder(root, second.Token); err != nil {
		t.Fatal(err)
	}
}
