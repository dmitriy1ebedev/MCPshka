package jobs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReapRemovesJobsOfVanishedWorkdirsUnlessReferenced(t *testing.T) {
	m, work := testManager(t)
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	var ids []string
	for _, name := range []string{"gone", "kept"} {
		dir := filepath.Join(work, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		j, err := m.Start(printf, []string{name}, dir)
		if err != nil {
			t.Fatal(err)
		}
		waitForTerminal(t, m, j.ID)
		ids = append(ids, j.ID)
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
	}
	// The guard cannot resolve a deleted worktree, as in production.
	m.SetLeaseGuard(func(path, _ string) error {
		if _, err := os.Stat(path); err != nil {
			return err
		}
		return nil
	})
	now := time.Now().Add(time.Hour)
	if n, err := m.Reap(0, now); err != nil || n != 0 {
		t.Fatalf("reaped without lease references: n=%d err=%v", n, err)
	}
	m.SetReapReferences(func() (map[string]bool, error) { return nil, errors.New("state unavailable") })
	if n, err := m.Reap(0, now); err != nil || n != 0 {
		t.Fatalf("reaped despite unreadable references: n=%d err=%v", n, err)
	}
	m.SetReapReferences(func() (map[string]bool, error) { return map[string]bool{ids[1]: true}, nil })
	if n, err := m.Reap(0, now); err != nil || n != 1 {
		t.Fatalf("removed=%d err=%v, want only the unreferenced job", n, err)
	}
	if _, err := m.Get(ids[0]); err == nil {
		t.Fatal("unreferenced vanished job kept")
	}
	if _, err := m.Get(ids[1]); err != nil {
		t.Fatal("lease-owned job receipt removed")
	}
}
