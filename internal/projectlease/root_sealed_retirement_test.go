package projectlease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sealedRetirementFixture(t *testing.T) (*Service, RootSealedRetirement) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("isolated root-only acceptance runs separately")
	}
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	state := filepath.Join(base, "state")
	for _, p := range []string{root, state} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	run := func(a ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, a...)...)
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatal(string(b), e)
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "-q")
	run("read-tree", "--empty")
	tree := run("write-tree")
	// Recovery previews require an immutable committed HEAD tree.
	run("config", "user.name", "MCPshka Test")
	run("config", "user.email", "mcpshka-test@example.invalid")
	run("commit", "--allow-empty", "-q", "-m", "fixture snapshot")
	if _, e := statestore.Open(filepath.Join(state, "state.db")); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	s := New(filepath.Join(state, "leases"), &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}})
	a, e := s.AcquireTracked(root, "legacy-owner", strings.Repeat("a", 32))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.database.Get("leases/" + filepath.Base(s.leasePath(root)))
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	p := RootSealedRetirement{Path: root, FenceRoot: root, GitCommonDir: filepath.Join(root, ".git"), LeaseID: a.Status.LeaseID, Generation: a.Status.Generation, ExecutionID: a.Status.ExecutionID, Owner: a.Status.Owner, RecordSHA256: hex.EncodeToString(sum[:]), SnapshotTree: tree, JournalID: strings.Repeat("b", 64), Authorization: "isolated operator acceptance"}
	for _, d := range []string{root, p.GitCommonDir} {
		if e := os.Chmod(d, 0500); e != nil {
			t.Fatal(e)
		}
	}
	return s, p
}
func TestRootSealedRetirementRejectsUnprivileged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root acceptance runs through managed job")
	}
	if _, e := (&Service{}).RetireRootSealed(RootSealedRetirement{}); e == nil {
		t.Fatal("unprivileged retirement admitted")
	}
}
func TestRootSealedRetirementRevokesOldTokenAndPreservesNewOwner(t *testing.T) {
	s, p := sealedRetirementFixture(t)
	old, e := s.readRecord(s.leasePath(p.Path))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RetireRootSealed(p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Heartbeat(p.Path, old.Token); e == nil {
		t.Fatal("old token survived retirement")
	}
	if _, e = s.AcquireTracked(p.Path, "new-owner", strings.Repeat("c", 32)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RetireRootSealed(p); e != nil {
		t.Fatal("idempotent retirement receipt lost", e)
	}
	got, e := s.Status(p.Path)
	if e != nil || !got.Held || got.Owner != "new-owner" {
		t.Fatal("replay removed new owner", got, e)
	}
}
func TestRootSealedRetirementHoldsUnsealedChangedAndLiveScopes(t *testing.T) {
	for _, kind := range []string{"unsealed", "changed-generation", "heartbeat", "live-fd", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, p := sealedRetirementFixture(t)
			switch kind {
			case "unsealed":
				if e := os.Chmod(p.Path, 0700); e != nil {
					t.Fatal(e)
				}
			case "changed-generation":
				p.Generation++
			case "heartbeat":
				r, e := s.readRecord(s.leasePath(p.Path))
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.Heartbeat(p.Path, r.Token); e != nil {
					t.Fatal(e)
				}
			case "live-fd":
				f, e := os.Open(filepath.Join(p.GitCommonDir, "config"))
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
			case "symlink":
				alias := filepath.Join(filepath.Dir(p.Path), "alias")
				if e := os.Symlink(p.Path, alias); e != nil {
					t.Fatal(e)
				}
				p.Path = alias
			}
			if _, e := s.RetireRootSealed(p); e == nil {
				t.Fatal("unsafe retirement admitted")
			}
			root := p.FenceRoot
			got, e := s.Status(root)
			if e != nil || !got.Held {
				t.Fatal("holder lost on HOLD", got, e)
			}
		})
	}
}

func TestRootSealedAdmissionClosesAcquireHeartbeatAndJobs(t *testing.T) {
	_, p := sealedRetirementFixture(t)
	if e := rootSealedWriterAdmissionForUID(p.Path, 994); e == nil {
		t.Fatal("unprivileged claim entered a sealed scope")
	}
	if e := rootSealedWriterAdmissionForUID(p.Path, 0); e != nil {
		t.Fatal("trusted operator cannot finish maintenance", e)
	}
}

func TestRootMaintenanceGateBlocksUnprivilegedLeaseAdmission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root gate acceptance runs through managed job")
	}
	s, root := heartbeatSQLiteService(t)
	a, e := s.AcquireTracked(root, "before-gate", strings.Repeat("d", 32))
	if e != nil {
		t.Fatal(e)
	}
	if ok, e := s.database.PutIfAbsent(s.writerGateKey(root), []byte("operator gate")); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if _, e := s.HeartbeatRefresh(root, a.Token); e == nil {
		t.Fatal("heartbeat passed closed gate")
	}
	if e := s.Guard(root, ""); e == nil {
		t.Fatal("native job passed closed gate")
	}
	if _, e := s.Release(root, a.Token); e != nil {
		t.Fatal("voluntary release denied", e)
	}
	if _, e := s.AcquireTracked(root, "late-owner", strings.Repeat("e", 32)); e == nil {
		t.Fatal("late claim passed closed gate")
	}
}

func TestRootGateOpeningRequiresPinnedOperatorSuccessor(t *testing.T) {
	s, p := sealedRetirementFixture(t)
	if _, e := s.RetireRootSealed(p); e != nil {
		t.Fatal(e)
	}
	a, e := s.AcquireTracked(p.Path, "operator-root:mcpshka-one-tree-test", strings.Repeat("f", 32))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.database.Get(s.writerGateKey(p.Path))
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	pin := hex.EncodeToString(sum[:])
	if e := s.OpenRootWriterGate(p.Path, "wrong", pin, strings.Repeat("e", 64)); e == nil {
		t.Fatal("wrong holder opened gate")
	}
	if e := s.OpenRootWriterGate(p.Path, a.Token, strings.Repeat("0", 64), strings.Repeat("e", 64)); e == nil {
		t.Fatal("wrong pin opened gate")
	}
	if e := s.OpenRootWriterGate(p.Path, a.Token, pin, strings.Repeat("e", 64)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.database.Get(s.writerGateKey(p.Path)); !os.IsNotExist(e) {
		t.Fatal("gate survived", e)
	}
}

func TestRootSealedRetirementAllowsInertUnprivilegedControlCWD(t *testing.T) {
	s, p := sealedRetirementFixture(t)
	if _, e := exec.LookPath("setpriv"); e != nil {
		t.Skip("setpriv unavailable")
	}
	if e := os.Chmod(filepath.Dir(p.Path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(p.Path, 0755); e != nil {
		t.Fatal(e)
	}
	c := exec.Command("setpriv", "--no-new-privs", "--reuid=65534", "--regid=65534", "--clear-groups", "/usr/bin/sleep", "30")
	c.Dir = p.Path
	if e := c.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	prefix := fmt.Sprintf("/proc/%d", c.Process.Pid)
	for i := 0; i < 100 && !unprivilegedSealedCWD(prefix); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !unprivilegedSealedCWD(prefix) {
		t.Fatal("child is not safely unprivileged")
	}
	if e := os.Chmod(p.Path, 0500); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RetireRootSealed(p); e != nil {
		t.Fatal("inert control cwd blocked retirement", e)
	}
}

func TestDetachedReadOnlyFDDoesNotFenceCurrentFiles(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "expired-reader")
	if e := os.WriteFile(p, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	entry := fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	dest, e := os.Readlink(entry)
	if e != nil {
		t.Fatal(e)
	}
	if detachedReadOnlyFD(entry, dest) {
		t.Fatal("live file ignored")
	}
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	dest, e = os.Readlink(entry)
	if e != nil {
		t.Fatal(e)
	}
	if !detachedReadOnlyFD(entry, dest) {
		t.Fatal("detached readonly inode treated as a writer")
	}
	if e := os.WriteFile(p, []byte("new"), 0600); e != nil {
		t.Fatal(e)
	}
	if !detachedReadOnlyFD(entry, dest) {
		t.Fatal("replacement inode confused with old reader")
	}
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(entry, p); e != nil {
		t.Fatal(e)
	}
	if detachedReadOnlyFD(entry, dest) {
		t.Fatal("live inode alias ignored")
	}
}
func TestDetachedWritableFDStillHolds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "writer")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	entry := fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	dest, e := os.Readlink(entry)
	if e != nil {
		t.Fatal(e)
	}
	if detachedReadOnlyFD(entry, dest) {
		t.Fatal("writable descriptor ignored")
	}
}

func TestRootSealedRetirementAllowsOnlyDetachedReadOnlyReader(t *testing.T) {
	s, p := sealedRetirementFixture(t)
	name := filepath.Join(p.Path, "expired-reader")
	if e := os.WriteFile(name, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(name)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e := os.Remove(name); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RetireRootSealed(p); e != nil {
		t.Fatal("deleted readonly inode blocked retirement", e)
	}
}

func TestRootSealedLegacyRecoveryAtomicJournalAndStaleToken(t *testing.T) {
	s, pinned := sealedRetirementFixture(t)
	old, err := s.readRecord(s.leasePath(pinned.Path))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewRootSealedRecovery(pinned.Path)
	if err != nil || !preview.Ready || preview.FencingState != "verified_sealed" {
		t.Fatalf("safe preview failed: %+v %v", preview, err)
	}
	if preview.Plan.RecordSHA256 != pinned.RecordSHA256 || preview.Plan.Generation != pinned.Generation {
		t.Fatal("preview did not pin actual previous holder")
	}
	plan := RootSealedRecovery{RootSealedRetirement: pinned,
		NewOwner: "operator-root:mcpshka-one-tree-test"}
	got, err := s.RecoverRootSealed(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Status.Held || got.Status.LeaseID != pinned.LeaseID ||
		got.Status.Generation != pinned.Generation+1 || got.Status.Owner != plan.NewOwner || got.Status.CommonGitDir != pinned.GitCommonDir ||
		got.Token == old.Token {
		t.Fatalf("invalid successor: %+v", got.Status)
	}
	if _, err := s.Heartbeat(pinned.Path, old.Token); err == nil {
		t.Fatal("legacy token retained authority")
	}
	if _, err := s.Release(pinned.Path, old.Token); err == nil {
		t.Fatal("legacy token released successor")
	}
	// A lost response replays the same successor; it never rotates again.
	replay, err := s.RecoverRootSealed(plan)
	if err != nil || replay.Token != got.Token || replay.Status.Generation != got.Status.Generation {
		t.Fatalf("replay must return the journaled successor: %+v %v", replay.Status, err)
	}
	other := plan
	other.SnapshotTree = strings.Repeat("0", 40)
	if _, err := s.RecoverRootSealed(other); err == nil {
		t.Fatal("different plan reused the recovery journal")
	}
	receipt, err := s.database.Get("project-lease-recoveries/v1/" + pinned.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(receipt), old.Token) || strings.Contains(string(receipt), got.Token) {
		t.Fatal("audit leaks secret holder tokens")
	}
	if _, err := s.database.Get(s.writerGateKey(pinned.Path)); err != nil {
		t.Fatal("OS handoff lifted writer gate", err)
	}
	current, err := s.ValidateHolder(pinned.Path, got.Token)
	if err != nil || current.Owner != plan.NewOwner {
		t.Fatal("successor token lost", err)
	}
	if _, err := s.Release(pinned.Path, got.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverRootSealed(plan); err == nil {
		t.Fatal("replay resurrected a released successor")
	}
}

func TestRootSealedLegacyRecoveryRefusesLiveProcessAndChangedOwner(t *testing.T) {
	for _, kind := range []string{"live-fd", "changed-generation", "bound-owner"} {
		t.Run(kind, func(t *testing.T) {
			s, pinned := sealedRetirementFixture(t)
			plan := RootSealedRecovery{RootSealedRetirement: pinned,
				NewOwner: "operator-root:mcpshka-one-tree-test"}
			switch kind {
			case "live-fd":
				fd, err := os.Open(filepath.Join(pinned.GitCommonDir, "config"))
				if err != nil {
					t.Fatal(err)
				}
				defer fd.Close()
			case "changed-generation":
				plan.Generation++
			case "bound-owner":
				old, err := s.readRecord(s.leasePath(pinned.Path))
				if err != nil {
					t.Fatal(err)
				}
				old.OwnerPID = 100
				raw, err := s.database.Get("leases/" + filepath.Base(s.leasePath(pinned.Path)))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := s.database.Replace("leases/"+filepath.Base(s.leasePath(pinned.Path)), raw, encoded); err != nil || !ok {
					t.Fatal(ok, err)
				}
			}
			if _, err := s.RecoverRootSealed(plan); err == nil {
				t.Fatal("unsafe recovery succeeded")
			}
			state, err := s.Status(pinned.Path)
			if err != nil || state.Owner != pinned.Owner || state.Generation != pinned.Generation {
				t.Fatal("HOLD lost old lease", state, err)
			}
		})
	}
}
