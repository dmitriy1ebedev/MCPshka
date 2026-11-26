package projectlease

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func interactiveFenceRepairFixture(t *testing.T) (*Service, string, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("isolated root-only fence repair acceptance runs separately")
	}
	base := t.TempDir()
	root, state := filepath.Join(base, "repo"), filepath.Join(base, "state")
	for _, dir := range []string{root, state, filepath.Join(state, "leases")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(state, "leases"), &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}})
	if err := os.Chown(s.dir, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	name := s.leasePath(root) + ".effect.lock"
	if err := os.WriteFile(name, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return s, root, name
}

func pinnedInteractiveFenceRepair(t *testing.T, s *Service, root string) InteractiveFenceRepairPlan {
	t.Helper()
	p, err := s.PreviewInteractiveFenceRepair(root, 65534, 65534)
	if err != nil {
		t.Fatal(err)
	}
	p.Authorization = "isolated active administrative grant"
	return p
}

func TestInteractiveFenceRepairPreservesInodeAndCompletedProof(t *testing.T) {
	s, root, name := interactiveFenceRepairFixture(t)
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	p := pinnedInteractiveFenceRepair(t, s, root)
	result, err := s.RepairInteractiveFence(p)
	if err != nil || result.State != "repaired" || result.FenceInode != p.FenceInode || result.JournalID != p.JournalID {
		t.Fatalf("repair failed: %+v %v", result, err)
	}
	after, err := os.Stat(name)
	if err != nil || !os.SameFile(before, after) || after.Mode() != 0600 || after.Size() != 0 {
		t.Fatal("repair replaced or changed lock content/mode", err)
	}
	st := after.Sys().(*syscall.Stat_t)
	if st.Uid != p.TargetUID || st.Gid != p.TargetGID || st.Nlink != 1 {
		t.Fatal("wrong repaired owner")
	}
	if err := s.writerAdmission(root); err != nil {
		t.Fatal("completed repair did not reopen admission", err)
	}
	journal, err := s.database.Get("project-fence-repairs/v1/" + p.JournalID)
	if err != nil || !bytes.Contains(journal, []byte(p.Authorization)) {
		t.Fatal("durable administrative journal missing", err)
	}
	leaseKey := "leases/" + filepath.Base(s.leasePath(root))
	foreign := []byte(`{"owner":"new-writer"}`)
	if err := s.database.Put(leaseKey, foreign); err != nil {
		t.Fatal(err)
	}
	if again, err := s.RepairInteractiveFence(p); err != nil || again != result {
		t.Fatal("completed proof did not reconcile", err)
	}
	current, err := s.database.Get(leaseKey)
	if err != nil || !bytes.Equal(current, foreign) {
		t.Fatal("completed replay changed a later holder", err)
	}
	if err := os.Chown(name, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepairInteractiveFence(p); err == nil {
		t.Fatal("completed journal certified a changed current owner")
	}
}

func TestInteractiveFenceRepairResumesPinnedGate(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-chown", true: "after-chown"}[changed], func(t *testing.T) {
			s, root, name := interactiveFenceRepairFixture(t)
			p := pinnedInteractiveFenceRepair(t, s, root)
			body, _ := json.Marshal(p)
			if err := s.database.Put(s.writerGateKey(root), body); err != nil {
				t.Fatal(err)
			}
			if changed {
				if err := os.Chown(name, int(p.TargetUID), int(p.TargetGID)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.Stat(name)
			if _, err := s.RepairInteractiveFence(p); err != nil {
				t.Fatal("pinned partial repair did not resume", err)
			}
			after, _ := os.Stat(name)
			if !os.SameFile(before, after) || (changed && before.Sys().(*syscall.Stat_t).Ctim != after.Sys().(*syscall.Stat_t).Ctim) {
				t.Fatal("resume replaced or chowned an already repaired inode")
			}
		})
	}
}

func TestInteractiveFenceRepairHoldsBusyAuthority(t *testing.T) {
	for _, problem := range []string{"flock", "lease", "foreign-gate", "journal-conflict", "live-project-reference"} {
		t.Run(problem, func(t *testing.T) {
			s, root, name := interactiveFenceRepairFixture(t)
			p := pinnedInteractiveFenceRepair(t, s, root)
			switch problem {
			case "flock":
				f, err := os.OpenFile(name, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "lease":
				if err := s.database.Put("leases/"+filepath.Base(s.leasePath(root)), []byte(`{"owner":"foreign"}`)); err != nil {
					t.Fatal(err)
				}
			case "foreign-gate":
				if err := s.database.Put(s.writerGateKey(root), []byte("foreign gate")); err != nil {
					t.Fatal(err)
				}
			case "journal-conflict":
				if err := s.database.Put("project-fence-repairs/v1/"+p.JournalID, []byte("foreign journal")); err != nil {
					t.Fatal(err)
				}
			case "live-project-reference":
				f, err := os.OpenFile(filepath.Join(root, "active"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
			}
			if problem == "flock" || problem == "lease" || problem == "foreign-gate" {
				if _, err := s.PreviewInteractiveFenceRepair(root, 65534, 65534); err == nil {
					t.Fatal("busy authority preview accepted")
				}
			}
			if _, err := s.RepairInteractiveFence(p); err == nil {
				t.Fatal("busy authority repaired")
			}
			info, _ := os.Stat(name)
			if info.Sys().(*syscall.Stat_t).Uid != 0 {
				t.Fatal("HOLD changed fence ownership")
			}
			if problem == "live-project-reference" {
				// writerAdmission trusts root, so prove the retained gate directly.
				if gate, err := s.database.Get(s.writerGateKey(root)); err != nil || len(gate) == 0 {
					t.Fatal("partial repair lost its writer gate", err)
				}
				key := "leases/" + filepath.Base(s.leasePath(root))
				if claimed, err := s.database.PutIfAbsentUnlessGate(key, []byte("late holder"), s.writerGateKey(root)); err != nil || claimed {
					t.Fatal("pending repair admitted a racing lease", err)
				}
			}
		})
	}
}

func TestInteractiveFenceRepairRejectsUntrustedPins(t *testing.T) {
	for _, problem := range []string{"inode", "directory", "authorization", "scope", "target", "journal", "replaced-inode"} {
		t.Run(problem, func(t *testing.T) {
			s, root, name := interactiveFenceRepairFixture(t)
			p := pinnedInteractiveFenceRepair(t, s, root)
			switch problem {
			case "inode":
				p.FenceInode++
			case "directory":
				p.DirectoryInode++
			case "authorization":
				p.Authorization = ""
			case "scope":
				p.Path += "/../repo"
			case "target":
				p.TargetUID = 65533
			case "replaced-inode":
				if err := os.Rename(name, name+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			p.JournalID = interactiveFenceRepairID(p)
			if problem == "journal" {
				p.JournalID = strings.Repeat("f", 64)
			}
			if _, err := s.RepairInteractiveFence(p); err == nil {
				t.Fatal("invalid pin admitted")
			}
			info, _ := os.Stat(name)
			if info.Sys().(*syscall.Stat_t).Uid != 0 {
				t.Fatal("invalid pin changed fence owner")
			}
		})
	}
}

func TestInteractiveFenceRepairPreviewRejectsUnsafeFiles(t *testing.T) {
	for _, problem := range []string{"symlink", "hardlink", "mode", "content", "directory-alias", "owner"} {
		t.Run(problem, func(t *testing.T) {
			s, root, name := interactiveFenceRepairFixture(t)
			var err error
			switch problem {
			case "symlink":
				err = os.Rename(name, name+".retained")
				if err == nil {
					err = os.Symlink(name+".retained", name)
				}
			case "hardlink":
				err = os.Link(name, name+".alias")
			case "mode":
				err = os.Chmod(name, 0644)
			case "content":
				err = os.WriteFile(name, []byte("unexpected content"), 0600)
			case "directory-alias":
				err = os.Symlink(s.dir, s.dir+".alias")
				s.dir += ".alias"
			case "owner":
				err = os.Chown(name, 65533, 65533)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.PreviewInteractiveFenceRepair(root, 65534, 65534); err == nil {
				t.Fatal("unsafe fence preview accepted")
			}
		})
	}
}

func TestInteractiveFenceRepairConcurrentApply(t *testing.T) {
	s, root, name := interactiveFenceRepairFixture(t)
	p := pinnedInteractiveFenceRepair(t, s, root)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := s.RepairInteractiveFence(p)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatal("no concurrent repair completed")
	}
	info, _ := os.Stat(name)
	st := info.Sys().(*syscall.Stat_t)
	if st.Ino != p.FenceInode || st.Uid != p.TargetUID || st.Gid != p.TargetGID {
		t.Fatal("concurrent repair replaced or misowned fence")
	}
	if _, err := s.database.Get("project-fence-repairs/v1/" + p.JournalID); err != nil {
		t.Fatal("concurrent completion has no journal", err)
	}
}
