package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func candidateRetirementFixture(t *testing.T) (*Controller, string, string, [2]string, *candidateStoppedProbe) {
	t.Helper()
	c, parent, owner, source, hash := candidateOwnerFixture(t)
	id := strings.Repeat("9", 32)
	probe := &candidateStoppedProbe{}
	if _, err := c.publishCandidate(context.Background(), parent, id, owner, source, hash, uint32(os.Geteuid()), probe); err != nil {
		t.Fatal(err)
	}
	var protected [2]string
	for i, name := range []string{"installed", "rollback"} {
		protected[i] = filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(protected[i], []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return c, parent, id, protected, probe
}

func TestCandidateRetirementDeleteReplayAndCrashRecovery(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "after_unlink"}[crash], func(t *testing.T) {
			c, parent, id, protected, probe := candidateRetirementFixture(t)
			ctx := context.Background()
			uid := uint32(os.Geteuid())
			plan, err := c.retireCandidateLocked(ctx, parent, id, "", protected, uid, probe)
			if err != nil || plan.State != "PLANNED" {
				t.Fatalf("plan %+v %v", plan, err)
			}
			if crash {
				plan.State = "DELETE_INTENT"
				raw, _ := json.Marshal(plan)
				if ok, err := c.db.PutIfAbsent(candidateRetirementKey(id), raw); err != nil || !ok {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(parent, id+".candidate")); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if i == 1 {
					probe.deny = true
					if err := os.Remove(protected[0]); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(protected[1], []byte("next rollback"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				got, err := c.retireCandidateLocked(ctx, parent, id, plan.Fingerprint, protected, uid, probe)
				if err != nil || got.State != "DELETED" {
					t.Fatalf("delete/replay %+v %v", got, err)
				}
			}
			if _, err := os.Stat(filepath.Join(parent, id+".candidate")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, id+".candidate"), []byte("replacement"), 0400); err != nil {
				t.Fatal(err)
			}
			if _, err := c.retireCandidateLocked(ctx, parent, id, plan.Fingerprint, protected, uid, probe); !errors.Is(err, ErrHold) {
				t.Fatal("replacement lost", err)
			}
		})
	}
}

func TestCandidateRetirementPreservesProtectedAndModifiedImages(t *testing.T) {
	for _, change := range []string{"installed", "rollback", "modified", "symlink", "fence", "fingerprint"} {
		t.Run(change, func(t *testing.T) {
			c, parent, id, protected, probe := candidateRetirementFixture(t)
			ctx := context.Background()
			uid := uint32(os.Geteuid())
			path := filepath.Join(parent, id+".candidate")
			plan, err := c.retireCandidateLocked(ctx, parent, id, "", protected, uid, probe)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "installed", "rollback":
				body, _ := os.ReadFile(path)
				i := 0
				if change == "rollback" {
					i = 1
				}
				if err := os.WriteFile(protected[i], body, 0600); err != nil {
					t.Fatal(err)
				}
			case "modified":
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(protected[0], path); err != nil {
					t.Fatal(err)
				}
			case "fence":
				probe.deny = true
			case "fingerprint":
				plan.Fingerprint = strings.Repeat("0", 64)
			}
			if _, err := c.retireCandidateLocked(ctx, parent, id, plan.Fingerprint, protected, uid, probe); !errors.Is(err, ErrHold) {
				t.Fatal("unsafe delete accepted", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("preserved object lost", err)
			}
		})
	}
}
