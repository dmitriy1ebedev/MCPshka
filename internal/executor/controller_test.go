package executor

import (
	"context"
	"errors"
	"mcpshka/internal/statestore"
	"mcpshka/internal/unitfence"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestPendingLaunchPreventsFenceAfterRestart(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	id := "dddddddddddddddddddddddddddddddd"
	if e = c.Reserve(context.Background(), p, id); e != nil {
		t.Fatal(e)
	}
	c = New(db)
	if e = c.Close(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if e = c.CompleteFence(context.Background(), p); !errors.Is(e, ErrHold) {
		t.Fatalf("unregistered launch fenced: %v", e)
	}
	if e = c.Reserve(context.Background(), p, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); !errors.Is(e, ErrHold) {
		t.Fatalf("revocation allowed launch: %v", e)
	}
	s, e := c.Inspect(context.Background(), p)
	if e != nil || s.State != "REVOKING" || len(s.Launches) != 1 || s.Launches[0].ReservedAt.IsZero() || s.Launches[0].ReservedAt.Before(s.CreatedAt) {
		t.Fatalf("lost durable intent: %+v %v", s, e)
	}
}
func TestCloseAndReserveRaceNeverLosesAcceptedLaunch(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var accepted bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		e := c.Reserve(context.Background(), p, "dddddddddddddddddddddddddddddddd")
		accepted = e == nil
		if e != nil && !errors.Is(e, ErrHold) {
			t.Error(e)
		}
	}()
	go func() {
		defer wg.Done()
		if e := c.Close(context.Background(), p); e != nil {
			t.Error(e)
		}
	}()
	wg.Wait()
	s, e := c.Inspect(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	if s.State != "REVOKING" || (accepted && len(s.Launches) != 1) {
		t.Fatalf("lost launch: accepted=%v state=%+v", accepted, s)
	}
}
func TestPermitABAAndGenerationAreRejected(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	wrong := p
	wrong.Generation++
	if e = c.Close(context.Background(), wrong); !errors.Is(e, ErrHold) {
		t.Fatalf("stale pin accepted: %v", e)
	}
	if e = c.Close(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if e = c.CompleteFence(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if e = c.Create(p); !errors.Is(e, ErrHold) {
		t.Fatalf("permit reused: %v", e)
	}
	if e = c.Reserve(context.Background(), p, "dddddddddddddddddddddddddddddddd"); !errors.Is(e, ErrHold) {
		t.Fatalf("old permit launch: %v", e)
	}
}

func TestGenerationCannotMintSecondPermit(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	q := p
	q.PermitID = "dddddddddddddddddddddddddddddddd"
	if e = c.Create(q); !errors.Is(e, ErrHold) {
		t.Fatalf("second permit for same generation accepted: %v", e)
	}
}
func TestRegistrationCannotBorrowAnotherLaunchCgroup(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	id := "dddddddddddddddddddddddddddddddd"
	if e = c.Reserve(context.Background(), p, id); e != nil {
		t.Fatal(e)
	}
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: "ffffffffffffffffffffffffffffffff", Cgroup: "/system.slice/mcpshka-executor-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	if e = c.Register(context.Background(), p, id, i); !errors.Is(e, ErrHold) {
		t.Fatalf("borrowed cgroup: %v", e)
	}
}

func TestUnitEvidenceRejectsIncarnationAndRestart(t *testing.T) {
	id := "dddddddddddddddddddddddddddddddd"
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: "ffffffffffffffffffffffffffffffff", Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	raw := "InvocationID=" + i.InvocationID + "\nControlGroup=" + i.Cgroup + "\nMainPID=3\nRestart=no\nKillMode=control-group\nActiveState=active\nLoadState=loaded\n"
	if e := unitfence.VerifyUnit(raw, i, false); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(raw, "Restart=no", "Restart=always", 1), strings.Replace(raw, i.InvocationID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", 1), raw + "MainPID=4\n", strings.Replace(raw, "MainPID=3", "MainPID=4", 1), strings.Replace(raw, i.Cgroup, "/system.slice/another.service", 1)} {
		if e := unitfence.VerifyUnit(bad, i, false); e == nil {
			t.Fatalf("unsafe unit evidence accepted: %s", bad)
		}
	}
}

func TestLateRegistrationStillRequiresExactFence(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	id := "dddddddddddddddddddddddddddddddd"
	if e = c.Reserve(context.Background(), p, id); e != nil {
		t.Fatal(e)
	}
	if e = c.Close(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: "ffffffffffffffffffffffffffffffff", Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	if e = c.Register(context.Background(), p, id, i); e != nil {
		t.Fatal(e)
	}
	b := &fenceStub{err: errors.New("descendant remains")}
	if e = c.Fence(context.Background(), p, b); !errors.Is(e, ErrHold) {
		t.Fatalf("failed fence accepted: %v", e)
	}
	s, e := c.Inspect(context.Background(), p)
	if e != nil || s.State != "REVOKING" || s.Launches[0].State != "REGISTERED" {
		t.Fatalf("false proof %+v %v", s, e)
	}
	b.err = nil
	if e = c.Fence(context.Background(), p, b); e != nil {
		t.Fatal(e)
	}
	s, e = c.Inspect(context.Background(), p)
	if e != nil || s.State != "FENCED" || b.id != id || b.identity != i {
		t.Fatalf("wrong fence %+v %v", s, e)
	}
}

type fenceStub struct {
	err      error
	id       string
	identity Identity
}

func (b *fenceStub) StopAndVerify(_ context.Context, id string, i Identity) error {
	b.id = id
	b.identity = i
	return b.err
}

func TestAuthorityCannotBeOpenedByUnprivilegedExecutor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires actual unprivileged test process")
	}
	if _, e := OpenAuthority(); !errors.Is(e, ErrHold) {
		t.Fatalf("unprivileged authority accepted: %v", e)
	}
}

func TestOnePermitNeverAdmitsTwoConcurrentWriters(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	if e = c.Reserve(context.Background(), p, "dddddddddddddddddddddddddddddddd"); e != nil {
		t.Fatal(e)
	}
	if e = c.Reserve(context.Background(), p, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); !errors.Is(e, ErrHold) {
		t.Fatalf("second writer admitted: %v", e)
	}
}
func TestLaunchIdentityCannotMoveToAnotherPermit(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LeaseID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1, ExecutionID: "cccccccccccccccccccccccccccccccc"}
	if e = c.Create(p); e != nil {
		t.Fatal(e)
	}
	id := "dddddddddddddddddddddddddddddddd"
	if e = c.Reserve(context.Background(), p, id); e != nil {
		t.Fatal(e)
	}
	q := p
	q.PermitID = "ffffffffffffffffffffffffffffffff"
	q.LeaseID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if e = c.Create(q); e != nil {
		t.Fatal(e)
	}
	if e = c.Reserve(context.Background(), q, id); !errors.Is(e, ErrHold) {
		t.Fatalf("unit identity reused across permits: %v", e)
	}
}

func TestDeletedCgroupEvidenceRejectsReplacementAndLiveDirectory(t *testing.T) {
	original := syscall.Stat_t{Dev: 1, Ino: 2, Nlink: 2}
	deleted := original
	deleted.Nlink = 2 // kernfs preserves nlink after deletion on target kernel
	if !unitfence.VerifiedDeletedCgroup(original, deleted, true, true) {
		t.Fatal("pinned deleted cgroup rejected")
	}
	live := original
	replacement := deleted
	replacement.Ino = 3
	if unitfence.VerifiedDeletedCgroup(original, live, true, false) || unitfence.VerifiedDeletedCgroup(original, replacement, true, true) || unitfence.VerifiedDeletedCgroup(original, deleted, false, true) {
		t.Fatal("unverified disappearance accepted")
	}
}

func TestAbsentUnitEvidenceRejectsReincarnationAndIncompleteEvidence(t *testing.T) {
	raw := "LoadState=not-found\nActiveState=inactive\nMainPID=0\nInvocationID=\nControlGroup=\nRestart=no\nKillMode=control-group\n"
	if !unitfence.AbsentUnitEvidence(raw) {
		t.Fatal("complete unit GC evidence rejected")
	}
	for _, bad := range []string{strings.Replace(raw, "InvocationID=\n", "Unknown=\n", 1), strings.Replace(raw, "ControlGroup=\n", "Unknown=\n", 1), strings.Replace(raw, "MainPID=0", "MainPID=77", 1), strings.Replace(raw, "InvocationID=\n", "InvocationID=abc\n", 1), strings.Replace(raw, "LoadState=not-found", "LoadState=loaded", 1), raw + "MainPID=0\n", strings.Replace(raw, "ControlGroup=\n", "ControlGroup=/system.slice/replacement.service\n", 1)} {
		if unitfence.AbsentUnitEvidence(bad) {
			t.Fatal("unsafe disappearance evidence accepted")
		}
	}
}
