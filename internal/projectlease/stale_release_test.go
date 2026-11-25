package projectlease

import (
	"encoding/json"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaleReleaseOnlyForGoneHolders(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("stale release is a root operator action")
	}
	s, root := heartbeatSQLiteService(t)
	if _, err := s.AcquireTracked(root, "dead-chat", strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewStaleRelease(root, time.Now()); err == nil || !strings.Contains(err.Error(), "may still be working") {
		t.Fatal("fresh lease offered for release", err)
	}
	bindGoneOwner(t, s, root)
	later := time.Now().Add(3 * time.Hour)
	p, err := s.PreviewStaleRelease(root, later)
	if err != nil || p.Owner != "dead-chat" || p.RecordSHA256 == "" {
		t.Fatal("stale lease not planned", p, err)
	}
	tampered := p
	tampered.RecordSHA256 = strings.Repeat("0", 64)
	if _, err := s.ReleaseStale(tampered, later); err == nil {
		t.Fatal("changed record released")
	}
	out, err := s.ReleaseStale(p, later)
	if err != nil || !out.Released {
		t.Fatal("stale lease not released", out, err)
	}
	if st, _ := s.Status(root); st.Held {
		t.Fatal("lease still held")
	}
	if replay, err := s.ReleaseStale(p, later); err != nil || !replay.Released {
		t.Fatal("release receipt not replayable", err)
	}
}

// A remote chat has no process on the host: it is released after a quiet
// period or once its agent_task ended; a bound process needs no wait.
func TestStaleReleaseRemoteHolderQuietPeriod(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("stale release is a root operator action")
	}
	ended := false
	defer func(old func(string, string, time.Time, time.Time) bool) { endedAgentHolder = old }(endedAgentHolder)
	endedAgentHolder = func(string, string, time.Time, time.Time) bool { return ended }
	s, root := heartbeatSQLiteService(t)
	if _, err := s.AcquireTracked(root, "chatgpt-diag", strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewStaleRelease(root, time.Now().Add(RemoteHolderQuiet-time.Minute)); err == nil || !strings.Contains(err.Error(), "remote_holder_recently_active") {
		t.Fatal("recently active remote holder offered for release", err)
	}
	if p, err := s.PreviewStaleRelease(root, time.Now().Add(RemoteHolderQuiet+time.Minute)); err != nil || p.Owner != "chatgpt-diag" {
		t.Fatal("quiet remote holder must be releasable without two hours", p, err)
	}
	ended = true
	if p, err := s.PreviewStaleRelease(root, time.Now()); err != nil || p.Owner != "chatgpt-diag" {
		t.Fatal("ended agent_task must be releasable at once", p, err)
	}
	ended = false
	bindGoneOwner(t, s, root)
	if p, err := s.PreviewStaleRelease(root, time.Now()); err != nil || p.Owner != "chatgpt-diag" {
		t.Fatal("proved dead owner must not wait", p, err)
	}
}

func TestStaleReleaseNestedOrphan(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root operator required")
	}
	s, base := heartbeatSQLiteService(t)
	root := filepath.Join(base, "repo", "bin", "old-worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(root, "dead-nested", strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	bindGoneOwner(t, s, root)
	later := time.Now().Add(3 * time.Hour)
	p, err := s.PreviewStaleRelease(root, later)
	if err != nil || p.Path != root {
		t.Fatal("orphan not planned at exact path", p, err)
	}
	if out, err := s.ReleaseStale(p, later); err != nil || !out.Released {
		t.Fatal("orphan not released", out, err)
	}
	if _, err := s.database.Get("leases/" + filepath.Base(s.leasePath(root))); err == nil {
		t.Fatal("orphan record remains")
	}
}

func TestStaleReleaseRejectsPathAliasesAndForeignRecord(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root operator required")
	}
	s, base := heartbeatSQLiteService(t)
	root := filepath.Join(base, "repo", "bin", "worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(root, "dead", strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(base, "repo"), alias); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(3 * time.Hour)
	for _, path := range []string{filepath.Join(alias, "bin", "worktree"), root + "/../worktree", "/outside/repos/absent"} {
		if _, err := s.PreviewStaleRelease(path, later); err == nil {
			t.Fatalf("unsafe scope accepted: %s", path)
		}
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, err := s.database.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	r.ProjectRoot = filepath.Join(base, "repo")
	changed, _ := json.Marshal(r)
	if err := s.database.Put(key, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewStaleRelease(root, later); err == nil {
		t.Fatal("foreign record accepted")
	}
}

func TestStaleReleaseRejectsLiveOwnerAndDeletedDirectoryHolder(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root operator required")
	}
	s, base := heartbeatSQLiteService(t)
	root := filepath.Join(base, "repo", "worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(root, "owner", strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	bindGoneOwner(t, s, root)
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, err := s.database.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	identity, err := jobs.CurrentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r.OwnerPID, r.OwnerStartTicks, r.OwnerBootID = identity.PID, identity.StartTicks, identity.BootID
	changed, _ := json.Marshal(r)
	if err := s.database.Put(key, changed); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(3 * time.Hour)
	if _, err := s.PreviewStaleRelease(root, later); err == nil || !strings.Contains(err.Error(), "owner process") {
		t.Fatal("live owner accepted", err)
	}
	if err := s.database.Put(key, raw); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewStaleRelease(root, later); err == nil || !strings.Contains(err.Error(), "live processes") {
		t.Fatal("deleted directory holder accepted", err)
	}
}

func TestStaleReleasePathRejectsEverySymlinkLevel(t *testing.T) {
	base := t.TempDir()
	s := &Service{policy: &policy.Policy{AllowedWorkdirs: []string{base}}}
	root := filepath.Join(base, "repo", "bin", "worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.staleReleasePath(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(base, "repo"), filepath.Join(base, "repo", "bin"), root} {
		moved := path + "-real"
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, path); err != nil {
			t.Fatal(err)
		}
		if err := s.staleReleasePath(root); err == nil {
			t.Fatalf("symlink at %s accepted", path)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "absent"), root); err != nil {
		t.Fatal(err)
	}
	if err := s.staleReleasePath(root); err == nil {
		t.Fatal("dangling alias treated as orphan")
	}
	for _, path := range []string{root + "/../worktree", base + "/" + strings.Repeat("nested/", 33) + "gone", filepath.Join(filepath.Dir(base), "foreign")} {
		if err := s.staleReleasePath(path); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
}

func TestStaleReleaseDoesNotResolveParentRepositoryLease(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root operator required")
	}
	s, base := heartbeatSQLiteService(t)
	parent := filepath.Join(base, "repo")
	root := filepath.Join(parent, "bin", "worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(parent, "parent-owner", strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(root, "nested-owner", strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	// The parent becoming a repository must not redirect the nested lease key.
	if out, err := exec.Command("git", "init", "--quiet", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	bindGoneOwner(t, s, root)
	later := time.Now().Add(3 * time.Hour)
	p, err := s.PreviewStaleRelease(root, later)
	if err != nil || p.Path != root || p.Owner != "nested-owner" {
		t.Fatal("nested scope redirected", p, err)
	}
	if out, err := s.ReleaseStale(p, later); err != nil || !out.Released {
		t.Fatal(out, err)
	}
	if st, err := s.Status(parent); err != nil || !st.Held || st.Owner != "parent-owner" {
		t.Fatal("parent lease affected", st, err)
	}
}

func TestStaleReleaseRejectsDeletedReadOnlyFileHolder(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root operator required")
	}
	s, base := heartbeatSQLiteService(t)
	root := filepath.Join(base, "repo", "worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(root, "dead", strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	bindGoneOwner(t, s, root)
	name := filepath.Join(root, "held")
	if err := os.WriteFile(name, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewStaleRelease(root, time.Now().Add(3*time.Hour)); err == nil || !strings.Contains(err.Error(), "live processes") {
		t.Fatal("unsealed deleted handle ignored", err)
	}
}

func bindGoneOwner(t *testing.T, s *Service, root string) {
	t.Helper()
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, err := s.database.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	identity, err := jobs.CurrentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r.OwnerPID, r.OwnerStartTicks, r.OwnerBootID = 2147483000, 1, identity.BootID
	raw, _ = json.Marshal(r)
	if err := s.database.Put(key, raw); err != nil {
		t.Fatal(err)
	}
}

// MCP-167: a lease on a removed worktree stays readable by its exact root, so
// the contract probe (project_lease_status) and stale release can drop it.
func TestStatusReadsLeaseOfRemovedWorktree(t *testing.T) {
	s, base := heartbeatSQLiteService(t)
	root := filepath.Join(base, "gone-worktree")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTracked(root, "dead-chat", strings.Repeat("d", 32)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(root)
	if err != nil || !st.Held || st.Owner != "dead-chat" || st.ProjectRoot != root {
		t.Fatal("removed worktree lease unreadable", st, err)
	}
	// After stale_release the record is gone: the after-probe sees it free.
	if st, err := s.Status(filepath.Join(base, "never-existed")); err != nil || st.Held {
		t.Fatal("missing path without lease must read as free", st, err)
	}
}
