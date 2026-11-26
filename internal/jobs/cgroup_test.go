package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeCgroup(t *testing.T, line string) string {
	t.Helper()
	dir := t.TempDir()
	oldRoot, oldSelf := cgroupRoot, selfCgroupFile
	cgroupRoot, selfCgroupFile = dir, filepath.Join(dir, "self")
	t.Cleanup(func() { cgroupRoot, selfCgroupFile = oldRoot, oldSelf })
	if err := os.WriteFile(selfCgroupFile, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOwnCgroupReadsUnifiedEntry(t *testing.T) {
	dir := fakeCgroup(t, "1:name=systemd:/old\n0::/system.slice/mcpshka.service\n")
	got, err := ownCgroup()
	if err != nil || got != filepath.Join(dir, "system.slice/mcpshka.service") {
		t.Fatalf("%q %v", got, err)
	}
	fakeCgroup(t, "1:cpu:/x\n")
	if _, err := ownCgroup(); err == nil {
		t.Fatal("v1-only host accepted")
	}
}

// Without Delegate=yes the subtree is not writable: jobs stay as before.
func TestDelegatedCgroupFallsBackWithoutDelegation(t *testing.T) {
	dir := fakeCgroup(t, "0::/system.slice/mcpshka.service\n")
	m := &Manager{}
	if _, err := m.EnableDelegatedCgroup(); err == nil {
		t.Fatal("missing cgroup accepted")
	}
	if m.HeavyCgroup() != "" {
		t.Fatal("heavy cgroup set after failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "system.slice/mcpshka.service/heavy")); !os.IsNotExist(err) {
		t.Fatal("tree touched without delegation")
	}
}

// A plain directory is not cgroupfs: writing cgroup.procs works on tmpfs, but
// starting into it must fail and keep the manager unchanged.
func TestDelegatedCgroupProvesStartBeforeUse(t *testing.T) {
	dir := fakeCgroup(t, "0::/svc\n")
	base := filepath.Join(dir, "svc")
	os.MkdirAll(base, 0o755)
	for _, f := range []string{"cgroup.subtree_control", "cgroup.procs"} {
		os.WriteFile(filepath.Join(base, f), nil, 0o644)
	}
	m := &Manager{}
	if _, err := m.EnableDelegatedCgroup(); err == nil {
		t.Fatal("non-cgroup directory accepted")
	}
	if m.HeavyCgroup() != "" {
		t.Fatal("heavy cgroup set without a proven start")
	}
}

// After the split the server lives in server/; the service pids limit is
// still read from the delegated parent.
func TestTasksHeadroomReadsParentOfServer(t *testing.T) {
	dir := fakeCgroup(t, "0::/svc/server\n")
	os.MkdirAll(filepath.Join(dir, "svc/server"), 0o755)
	os.WriteFile(filepath.Join(dir, "svc/pids.current"), []byte("95\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "svc/pids.max"), []byte("100\n"), 0o644)
	if cur, max, ok := TasksHeadroom(); !ok || cur != 95 || max != 100 {
		t.Fatalf("%d %d %v", cur, max, ok)
	}
	if checkTasksHeadroom() == nil {
		t.Fatal("exhausted service cgroup admitted")
	}
}

// A heavy job that starts the runtime must never split heavy/ itself
// (15.11: controllers on heavy/ made every later job start EBUSY).
func TestDelegatedCgroupRefusesInsideHeavy(t *testing.T) {
	dir := fakeCgroup(t, "0::/svc/heavy\n")
	heavy := filepath.Join(dir, "svc/heavy")
	os.MkdirAll(heavy, 0o755)
	for _, f := range []string{"cgroup.subtree_control", "cgroup.procs"} {
		os.WriteFile(filepath.Join(heavy, f), nil, 0o644)
	}
	m := &Manager{}
	if _, err := m.EnableDelegatedCgroup(); err == nil {
		t.Fatal("split inside heavy/ accepted")
	}
	if _, err := os.Stat(filepath.Join(heavy, "heavy")); !os.IsNotExist(err) {
		t.Fatal("heavy/ was split")
	}
}
