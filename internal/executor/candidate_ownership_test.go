package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCandidateStoreLockSerializesIndependentCallers(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- withCandidateStoreLock(ctx, parent, uid, func() error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var calls atomic.Int32
	wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err := withCandidateStoreLock(wait, parent, uid, func() error { calls.Add(1); return nil })
	stop()
	close(release)
	if first := <-done; first != nil {
		t.Fatal(first)
	}
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("concurrent caller entered: %v calls=%d", err, calls.Load())
	}
	if err := withCandidateStoreLock(ctx, parent, uid, func() error { calls.Add(1); return nil }); err != nil || calls.Load() != 1 {
		t.Fatalf("lock not released: %v calls=%d", err, calls.Load())
	}
}

func TestCandidateStoreLockRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "foreign"), filepath.Join(parent, ".store.lock")); err != nil {
		t.Fatal(err)
	}
	if err := withCandidateStoreLock(context.Background(), parent, uint32(os.Geteuid()), func() error { t.Fatal("unsafe lock accepted"); return nil }); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
}

type candidateStoppedProbe struct {
	calls int
	deny  bool
}

func (p *candidateStoppedProbe) VerifyStopped(context.Context, string, Identity) error {
	p.calls++
	if p.deny {
		return ErrHold
	}
	return nil
}

func candidateOwnerFixture(t *testing.T) (*Controller, string, CandidateOwner, *os.File, string) {
	t.Helper()
	c, parent := protectedBirthFixture(t)
	pin := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	launch := strings.Repeat("d", 32)
	identity := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + launch + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	ctx := context.Background()
	for _, err := range []error{c.Create(pin), c.Reserve(ctx, pin, launch), c.Register(ctx, pin, launch, identity)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := c.recordResult(ctx, ExecutionResult{Pin: pin, LaunchID: launch, Identity: identity, ExitCode: 0, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source")
	body := []byte("bounded candidate image")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	sum := sha256.Sum256(body)
	return c, parent, CandidateOwner{Pin: pin, LaunchID: launch, PermitCommit: strings.Repeat("e", 40)}, f, hex.EncodeToString(sum[:])
}

func TestCandidateOwnershipBirthReplayAndFreshFence(t *testing.T) {
	c, parent, owner, source, digest := candidateOwnerFixture(t)
	probe := &candidateStoppedProbe{}
	id := strings.Repeat("1", 32)
	born, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), probe)
	if err != nil {
		t.Fatal(err)
	}
	if born.State != "READY" || born.SHA256 != digest || born.Artifact.Inode == 0 {
		t.Fatalf("unproved birth: %+v", born)
	}
	copy, err := os.ReadFile(filepath.Join(parent, id+".candidate"))
	if err != nil || string(copy) != "bounded candidate image" {
		t.Fatalf("copy: %q %v", copy, err)
	}
	// Source changes cannot mutate the newly born, private copy.
	if err := os.WriteFile(source.Name(), []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), probe)
	if err != nil || again != born || probe.calls != 2 {
		t.Fatalf("replay: %+v %v calls=%d", again, err, probe.calls)
	}
	probe.deny = true
	if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), probe); !errors.Is(err, ErrHold) {
		t.Fatal("stale fence accepted", err)
	}
}

func TestCandidateOwnershipNeverAdoptsExistingOrReplacedFile(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "replacement"}[replace], func(t *testing.T) {
			c, parent, owner, source, digest := candidateOwnerFixture(t)
			id := strings.Repeat("2", 32)
			path := filepath.Join(parent, id+".candidate")
			probe := &candidateStoppedProbe{}
			if replace {
				if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), probe); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+".preserved"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("foreign"), 0400); err != nil {
				t.Fatal(err)
			}
			if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), probe); !errors.Is(err, ErrHold) {
				t.Fatal("adopted unexplained file", err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "foreign" {
				t.Fatalf("foreign file changed: %q %v", body, err)
			}
		})
	}
}

func TestCandidateOwnershipRejectsWrongGenerationBeforeCreation(t *testing.T) {
	c, parent, owner, source, digest := candidateOwnerFixture(t)
	owner.Pin.Generation++
	id := strings.Repeat("3", 32)
	if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), &candidateStoppedProbe{}); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(parent, id+".candidate")); !os.IsNotExist(err) {
		t.Fatal("invalid owner produced file", err)
	}
}

func TestCandidateOwnershipWrongDigestDoesNotCreate(t *testing.T) {
	c, parent, owner, source, _ := candidateOwnerFixture(t)
	id := strings.Repeat("4", 32)
	if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, strings.Repeat("0", 64), uint32(os.Geteuid()), &candidateStoppedProbe{}); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(parent, id+".candidate")); !os.IsNotExist(err) {
		t.Fatal("bad digest stranded file", err)
	}
}

func TestCandidateOwnershipProvenanceIsSupervisorCopy(t *testing.T) {
	c, parent, owner, source, digest := candidateOwnerFixture(t)
	born, err := c.publishCandidate(context.Background(), parent, strings.Repeat("5", 32), owner, source, digest, uint32(os.Geteuid()), &candidateStoppedProbe{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(born)
	if !strings.Contains(string(raw), "supervisor-copy-v1") {
		t.Fatal("permit association overstated as build provenance", string(raw))
	}
}

func TestCandidateOwnershipRecoveryFromCreated(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "synced", true: "partial"}[partial], func(t *testing.T) {
			c, parent, owner, source, digest := candidateOwnerFixture(t)
			id := strings.Repeat("6", 32)
			born, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), &candidateStoppedProbe{})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(born)
			created := born
			created.State = "CREATED"
			next, _ := json.Marshal(created)
			if ok, err := c.db.Replace(candidateOwnershipKey(id), raw, next); err != nil || !ok {
				t.Fatal(ok, err)
			}
			path := filepath.Join(parent, id+".candidate")
			if partial {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, 5); err != nil {
					t.Fatal(err)
				}
			} else {
				source.Close()
				source = nil
			} // Fully synced output recovers without its old input FD.
			recovered, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), &candidateStoppedProbe{})
			if err != nil || recovered.State != "READY" || recovered.SHA256 != digest || recovered.Artifact.Inode != born.Artifact.Inode {
				t.Fatalf("recovery: %+v %v", recovered, err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "bounded candidate image" {
				t.Fatal(string(body), err)
			}
		})
	}
}

func TestCandidateOwnershipCreateWindowRemainsUnknown(t *testing.T) {
	c, parent, owner, source, digest := candidateOwnerFixture(t)
	id := strings.Repeat("7", 32)
	born, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), &candidateStoppedProbe{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(born)
	unknown := born
	unknown.State = "CREATE_INTENT"
	unknown.Artifact = candidateArtifactIdentity{}
	next, _ := json.Marshal(unknown)
	if ok, err := c.db.Replace(candidateOwnershipKey(id), raw, next); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, digest, uint32(os.Geteuid()), &candidateStoppedProbe{}); !errors.Is(err, ErrHold) {
		t.Fatal("unproved creation adopted", err)
	}
}
