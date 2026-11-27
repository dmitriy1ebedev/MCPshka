package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gcFixture(t *testing.T, m *Manager, work, id string, state State, days int, name string) string {
	t.Helper()
	j := &Job{ID: id, Workdir: work, State: state, FinishedAt: time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)}
	m.jobs[id] = j
	if err := m.persist(snapshot(j)); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(m.root, id, "tmp", name)
	if err := os.MkdirAll(build, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "pkg.a"), []byte("retained cache payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root, id, "stdout.log"), []byte("important receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	return build
}

func TestArtifactGCPreviewThenApplyPreservesReceipts(t *testing.T) {
	m, work := testManager(t)
	eligible := gcFixture(t, m, work, "gc-done-old", Done, 4, "go-build100")
	failedOld := gcFixture(t, m, work, "gc-failed-old", TimedOut, 5, "go-build200")
	recent := gcFixture(t, m, work, "gc-done-recent", Done, 2, "go-build300")
	failYoung := gcFixture(t, m, work, "gc-failed-young", Failed, 3, "go-build400")
	unknown := gcFixture(t, m, work, "gc-unknown-old", Unknown, 90, "go-build500")
	workflow := gcFixture(t, m, work, "workflow-"+strings.Repeat("a", 32), Done, 90, "go-build600")
	referenced := gcFixture(t, m, work, "gc-referenced", Done, 90, "go-build700")
	m.SetReapReferences(func() (map[string]bool, error) { return map[string]bool{"gc-referenced": true}, nil })
	m.SetLeaseGuard(func(string, string) error { return nil })
	preview, err := m.ArtifactGC(time.Now().UTC(), false, "")
	if err != nil || preview.Eligible != 2 || preview.Deleted != 0 || preview.PlanSHA256 == "" {
		t.Fatalf("unsafe preview: %+v %v", preview, err)
	}
	if _, err := os.Stat(eligible); err != nil {
		t.Fatal("preview deleted a tree", err)
	}
	hold, err := m.ArtifactGC(time.Now().UTC(), true, strings.Repeat("0", 64))
	if err != nil || hold.State != "HOLD" || hold.Deleted != 0 {
		t.Fatalf("digest mismatch deleted: %+v %v", hold, err)
	}
	applied, err := m.ArtifactGC(time.Now().UTC(), true, preview.PlanSHA256)
	if err != nil || applied.Deleted != 2 || applied.DeletedBytes == 0 {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	for _, gone := range []string{eligible, failedOld} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("cache still present: %q %v", gone, err)
		}
	}
	for _, kept := range []string{recent, failYoung, unknown, workflow, referenced} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("protected cache deleted: %s %v", kept, err)
		}
	}
	for _, id := range []string{"gc-done-old", "gc-failed-old"} {
		if _, err := m.Get(id); err != nil {
			t.Fatalf("job receipt lost: %s %v", id, err)
		}
		data, err := os.ReadFile(filepath.Join(m.root, id, "stdout.log"))
		if err != nil || string(data) != "important receipt" {
			t.Fatalf("output receipt lost: %s %v", id, err)
		}
	}
	again, err := m.ArtifactGC(time.Now().UTC(), true, "")
	if err != nil || again.Deleted != 0 {
		t.Fatalf("repeat must be safe: %+v %v", again, err)
	}
}

func TestArtifactGCRefusesSymlinksAndHardlinks(t *testing.T) {
	m, work := testManager(t)
	m.SetReapReferences(func() (map[string]bool, error) { return map[string]bool{}, nil })
	m.SetLeaseGuard(func(string, string) error { return nil })
	external := t.TempDir()
	evil := gcFixture(t, m, work, "gc-symlink", Done, 45, "go-build800")
	if err := os.RemoveAll(evil); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, evil); err != nil {
		t.Fatal(err)
	}
	hard := gcFixture(t, m, work, "gc-hardlink", Done, 45, "go-build900")
	original := filepath.Join(hard, "pkg.a")
	if err := os.Link(original, filepath.Join(external, "linked.a")); err != nil {
		t.Fatal(err)
	}
	parentRedirect := gcFixture(t, m, work, "gc-parent-link", Done, 45, "go-build1000")
	parentTmp := filepath.Dir(parentRedirect)
	if err := os.RemoveAll(parentTmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, parentTmp); err != nil {
		t.Fatal(err)
	}
	preview, err := m.ArtifactGC(time.Now().UTC(), false, "")
	if err != nil || preview.Eligible != 0 {
		t.Fatalf("unsafe links shortlisted: %+v %v", preview, err)
	}
	applied, err := m.ArtifactGC(time.Now().UTC(), true, preview.PlanSHA256)
	if err != nil || applied.Deleted != 0 {
		t.Fatalf("unsafe removal: %+v %v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(external, "linked.a")); err != nil {
		t.Fatal("hard link target affected", err)
	}
}

func TestArtifactGCFailClosedOnUnavailableAuthorityAndHeldLease(t *testing.T) {
	m, work := testManager(t)
	dir := gcFixture(t, m, work, "gc-authority", Done, 40, "go-build1200")
	m.SetReapReferences(func() (map[string]bool, error) { return nil, errors.New("lease records unavailable") })
	report, err := m.ArtifactGC(time.Now().UTC(), true, "")
	if err != nil || report.Deleted != 0 || report.State != "HOLD" {
		t.Fatalf("authority fail-open: %+v %v", report, err)
	}
	m.SetReapReferences(func() (map[string]bool, error) { return map[string]bool{}, nil })
	m.SetLeaseGuard(func(string, string) error { return errors.New("writer held") })
	report, err = m.ArtifactGC(time.Now().UTC(), true, "")
	if err != nil || report.Deleted != 0 {
		t.Fatalf("lease fail-open: %+v %v", report, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactGCWithoutAuthoritiesFailsClosed(t *testing.T) {
	m, work := testManager(t)
	build := gcFixture(t, m, work, "gc-unconfigured", Done, 60, "go-build1900")
	now := time.Now().UTC()
	preview, err := m.ArtifactGC(now, false, "")
	if err != nil || preview.State != "HOLD" || preview.Eligible != 0 {
		t.Fatalf("missing references must fail closed: %+v %v", preview, err)
	}
	m.SetReapReferences(func() (map[string]bool, error) { return nil, nil })
	preview, err = m.ArtifactGC(now, false, "")
	if err != nil || preview.State != "HOLD" {
		t.Fatalf("nil references must fail closed: %+v %v", preview, err)
	}
	m.SetReapReferences(func() (map[string]bool, error) { return map[string]bool{}, nil })
	preview, err = m.ArtifactGC(now, false, "")
	if err != nil || preview.State != "HOLD" || preview.Reason != "project lease authority not configured" {
		t.Fatalf("missing lease guard must fail closed: %+v %v", preview, err)
	}
	if _, err := os.Stat(build); err != nil {
		t.Fatal("gc touched tree without authorities:", err)
	}
}

// 24.11: timed-out tests of removed worktrees kept 858 MB of compiler
// scratch because a vanished workdir was treated as protection.
func TestArtifactGCCollectsScratchOfRemovedWorktree(t *testing.T) {
	m, work := testManager(t)
	gone := filepath.Join(work, "removed-worktree")
	scratch := gcFixture(t, m, gone, "gc-vanished", TimedOut, 5, "go-build900")
	m.SetReapReferences(func() (map[string]bool, error) { return map[string]bool{}, nil })
	m.SetLeaseGuard(func(string, string) error { return errors.New("no project at this path") })
	preview, err := m.ArtifactGC(time.Now().UTC(), false, "")
	if err != nil || preview.Eligible != 1 {
		t.Fatalf("%+v %v", preview, err)
	}
	if applied, err := m.ArtifactGC(time.Now().UTC(), true, preview.PlanSHA256); err != nil || applied.Deleted != 1 {
		t.Fatalf("%+v %v", applied, err)
	}
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatal("scratch kept")
	}
}
