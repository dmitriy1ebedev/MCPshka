package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ownedGitBranchFixture(t *testing.T) (*Controller, string, RepositoryIdentity, CandidateOwner) {
	t.Helper()
	c, parent, owner, _, _ := candidateOwnerFixture(t)
	born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	owner.PermitCommit, _ = protectedTestCommit(t, born.Repository.CommonDir)
	return c, parent, born.Repository, owner
}

func TestOwnedGitBranchBirthReplayAndFreshFence(t *testing.T) {
	c, parent, repo, owner := ownedGitBranchFixture(t)
	probe := &candidateStoppedProbe{}
	id := strings.Repeat("1", 32)
	ctx := context.Background()
	born, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "development/r1", owner, uint32(os.Geteuid()), probe, nil)
	if err != nil || born.State != "READY" || born.Inode == 0 {
		t.Fatalf("birth: %+v %v", born, err)
	}
	again, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "development/r1", owner, uint32(os.Geteuid()), probe, nil)
	if err != nil || again != born {
		t.Fatalf("replay: %+v %v", again, err)
	}
	probe.deny = true
	if _, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "development/r1", owner, uint32(os.Geteuid()), probe, nil); !errors.Is(err, ErrHold) {
		t.Fatal("borrowed old fence", err)
	}
}

func TestOwnedGitBranchCrashAfterPublishRecoversSameInode(t *testing.T) {
	c, parent, repo, owner := ownedGitBranchFixture(t)
	id := strings.Repeat("2", 32)
	crash := errors.New("crash after publication")
	ctx := context.Background()
	born, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, func() error { return crash })
	if !errors.Is(err, crash) || born.State != "CREATED" {
		t.Fatal(born, err)
	}
	again, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, func() error { return nil })
	if err != nil || again.State != "READY" || again.Inode != born.Inode {
		t.Fatal("recovery recreated branch", again, err)
	}
}

func TestOwnedGitBranchNeverAdoptsUnknownOrReplacedRef(t *testing.T) {
	for _, kind := range []string{"existing", "replacement", "symlink", "interrupted-create"} {
		t.Run(kind, func(t *testing.T) {
			c, parent, repo, owner := ownedGitBranchFixture(t)
			id := strings.Repeat("3", 32)
			path := filepath.Join(repo.CommonDir, "refs/heads/task-r1")
			ctx := context.Background()
			if kind == "existing" {
				if err := os.WriteFile(path, []byte(owner.PermitCommit+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "interrupted-create" {
				if err := os.WriteFile(filepath.Join(repo.CommonDir, "refs/heads/.birth-"+id), []byte(owner.PermitCommit+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+".preserved"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(path+".preserved", path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(owner.PermitCommit+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.prepareOwnedGitBranch(ctx, parent, id, repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); !errors.Is(err, ErrHold) {
				t.Fatal("adopted unknown inode", err)
			}
			if _, err := os.Lstat(path); kind != "interrupted-create" && err != nil {
				t.Fatal("removed unknown ref", err)
			}
			if kind == "interrupted-create" {
				if _, err := c.prepareOwnedGitBranch(ctx, parent, strings.Repeat("9", 32), repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); !errors.Is(err, ErrHold) {
					t.Fatal("borrowed name from interrupted birth", err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("published second resource", err)
				}
			}
		})
	}
}

func TestOwnedGitBranchRejectsProtectedNamesAndUnfencedOwner(t *testing.T) {
	c, parent, repo, owner := ownedGitBranchFixture(t)
	for _, branch := range []string{"main", "master", "HEAD", "../main", "task.lock", "development/.hidden"} {
		if _, err := c.prepareOwnedGitBranch(context.Background(), parent, strings.Repeat("4", 32), repo, branch, owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); !errors.Is(err, ErrHold) {
			t.Fatal(branch, err)
		}
	}
	owner.Pin.Generation++
	if _, err := c.prepareOwnedGitBranch(context.Background(), parent, strings.Repeat("4", 32), repo, "task-r1", owner, uint32(os.Geteuid()), &candidateStoppedProbe{}, nil); !errors.Is(err, ErrHold) {
		t.Fatal("wrong owner admitted", err)
	}
}
