package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"mcpshka/internal/integrationqueue"
)

// OwnedGitBranch records an actual root-issued branch birth, not ownership
// inferred from a branch name, queue owner, or a pre-existing checkout.
type OwnedGitBranch struct {
	Version    int                `json:"version"`
	ID         string             `json:"id"`
	Repository RepositoryIdentity `json:"repository"`
	Ref        string             `json:"ref"`
	Commit     string             `json:"commit"`
	Owner      CandidateOwner     `json:"owner"`
	State      string             `json:"state"`
	Device     uint64             `json:"device"`
	Inode      uint64             `json:"inode"`
}

func ownedGitBranchKey(id string) string { return "executor/owned-git-branches/" + id + ".json" }

func ownedGitBranchNameKey(repo, ref string) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + ref))
	return "executor/owned-git-branch-names/" + hex.EncodeToString(sum[:]) + ".json"
}

// PrepareOwnedGitBranch is an operator action for a requested development
// branch in an existing protected store. It creates no worktree and grants no
// execution, integration, deletion, or authority over legacy source branches.
func PrepareOwnedGitBranch(ctx context.Context, storeID, id, permitID, branch string) (OwnedGitBranch, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(storeID) || !idPattern.MatchString(id) || !integrationqueue.ValidSourceBranch(branch) {
		return OwnedGitBranch{}, ErrHold
	}
	c, m, err := OpenPermit(permitID)
	if err != nil || m.Purpose != "project-test-v1" || !validSource(m.Source) {
		return OwnedGitBranch{}, ErrHold
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil || s.Result == nil {
		return OwnedGitBranch{}, ErrHold
	}
	repo, err := protectedDirectoryIdentity(filepath.Join(ProtectedRepositoryRoot, storeID+".git"), 0)
	if err != nil {
		return OwnedGitBranch{}, ErrHold
	}
	owner := CandidateOwner{m.Pin, s.Result.LaunchID, m.Source.Commit}
	var result OwnedGitBranch
	err = c.withIntegrationLock(ctx, repo, func() error {
		if err := verifyProtectedRepositoryStorage(ctx, repo); err != nil {
			return err
		}
		return c.WithPermitLock(ctx, owner.Pin, func() error {
			var err error
			result, err = c.prepareOwnedGitBranch(ctx, ProtectedRepositoryRoot, id, repo, branch, owner, 0, SystemdFence{}, nil)
			return err
		})
	})
	return result, err
}

// Caller holds the root repository lock before the permit lock. The private
// parent/UID/observer parameters permit real filesystem tests without root.
func (c *Controller) prepareOwnedGitBranch(ctx context.Context, parent, id string, repo RepositoryIdentity, branch string, owner CandidateOwner, uid uint32, observer stoppedObserver, afterPublish func() error) (OwnedGitBranch, error) {
	if !integrationqueue.ValidSourceBranch(branch) {
		return OwnedGitBranch{}, ErrHold
	}
	return c.prepareOwnedGitRef(ctx, parent, id, repo, "refs/heads/"+branch, owner, uid, observer, afterPublish)
}

func validOwnedGitRef(ref string) bool {
	return (strings.HasPrefix(ref, "refs/heads/") && integrationqueue.ValidSourceBranch(strings.TrimPrefix(ref, "refs/heads/"))) ||
		(strings.HasPrefix(ref, "refs/mcpshka/recovery/owned-branches/") && idPattern.MatchString(strings.TrimPrefix(ref, "refs/mcpshka/recovery/owned-branches/")))
}

// Recovery refs use the same inode-before-publication journal. They cannot
// match an integration source branch and therefore cannot acquire cleanup authority.
func (c *Controller) prepareOwnedGitRef(ctx context.Context, parent, id string, repo RepositoryIdentity, ref string, owner CandidateOwner, uid uint32, observer stoppedObserver, afterPublish func() error) (OwnedGitBranch, error) {
	if ctx.Err() != nil {
		return OwnedGitBranch{}, ctx.Err()
	}
	if c.db == nil || !idPattern.MatchString(id) || !validOwnedGitRef(ref) {
		return OwnedGitBranch{}, ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, repo, uid); err != nil {
		return OwnedGitBranch{}, ErrHold
	}
	if err := c.candidateOwnerStopped(ctx, owner, observer); err != nil {
		return OwnedGitBranch{}, ErrHold
	}
	root, err := os.OpenRoot(repo.CommonDir)
	if err != nil {
		return OwnedGitBranch{}, ErrHold
	}
	defer root.Close()
	if err := validateProtectedObjects(ctx, root, owner.PermitCommit); err != nil {
		return OwnedGitBranch{}, ErrHold
	}
	want := OwnedGitBranch{Version: 1, ID: id, Repository: repo, Ref: ref, Commit: owner.PermitCommit, Owner: owner, State: "CREATE_INTENT"}
	// A lost birth must reserve the name even before the ref exists.
	reservationKey := ownedGitBranchNameKey(repo.CommonDir, want.Ref)
	reservation, _ := json.Marshal(want)
	reserved, err := c.db.PutIfAbsent(reservationKey, reservation)
	if err != nil {
		return want, err
	}
	if !reserved {
		prior, err := c.db.GetContext(ctx, reservationKey)
		if err != nil || !bytes.Equal(prior, reservation) {
			return want, ErrHold
		}
	}
	raw, err := c.db.GetContext(ctx, ownedGitBranchKey(id))
	born := want
	if os.IsNotExist(err) {
		if _, e := root.Lstat(want.Ref); !os.IsNotExist(e) {
			return born, ErrHold
		}
		raw, _ = json.Marshal(want)
		fresh, e := c.db.PutIfAbsent(ownedGitBranchKey(id), raw)
		if e != nil {
			return born, e
		}
		if !fresh {
			return born, ErrHold
		}
	} else {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err != nil || len(raw) > 8192 || d.Decode(&born) != nil || d.Decode(new(any)) != io.EOF {
			return born, ErrHold
		}
		pins := born
		pins.State = want.State
		pins.Device = 0
		pins.Inode = 0
		if pins != want {
			return born, ErrHold
		}
	}
	if born.State == "READY" {
		return born, inspectOwnedGitBranch(root, born, born.Ref, uid)
	}
	dir := filepath.Dir(born.Ref)
	tmp := filepath.Join(dir, ".birth-"+id)
	if born.State == "CREATE_INTENT" {
		if born.Device != 0 || born.Inode != 0 {
			return born, ErrHold
		}
		if _, e := root.Lstat(born.Ref); !os.IsNotExist(e) {
			return born, ErrHold
		}
		if err := root.MkdirAll(dir, 0700); err != nil {
			return born, ErrHold
		}
		// Persist every directory edge, not only the final ref directory.
		for at := dir; ; at = filepath.Dir(at) {
			d, err := root.Open(at)
			if err != nil {
				return born, ErrHold
			}
			se := d.Sync()
			ce := d.Close()
			if se != nil || ce != nil {
				return born, ErrHold
			}
			if at == "." {
				break
			}
		}
		f, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return born, ErrHold
		}
		_, we := io.WriteString(f, born.Commit+"\n")
		se := f.Sync()
		info, ie := f.Stat()
		ce := f.Close()
		if we != nil || se != nil || ie != nil || ce != nil {
			return born, ErrHold
		}
		if err := syncProtectedRefDirectory(root, tmp); err != nil {
			return born, ErrHold
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uid || st.Nlink != 1 || uint64(st.Dev) != repo.Device {
			return born, ErrHold
		}
		born.State = "CREATED"
		born.Device = uint64(st.Dev)
		born.Inode = st.Ino
		next, _ := json.Marshal(born)
		ok, e = c.db.Replace(ownedGitBranchKey(id), raw, next)
		if e != nil || !ok {
			return born, ErrHold
		}
		raw = next
	}
	if born.State != "CREATED" || born.Device != repo.Device || born.Inode == 0 {
		return born, ErrHold
	}
	if _, e := root.Lstat(born.Ref); os.IsNotExist(e) {
		if err := inspectOwnedGitBranch(root, born, tmp, uid); err != nil {
			return born, err
		}
		d, e := root.Open(dir)
		if e != nil {
			return born, ErrHold
		}
		e = unix.Renameat2(int(d.Fd()), filepath.Base(tmp), int(d.Fd()), filepath.Base(born.Ref), unix.RENAME_NOREPLACE)
		se := d.Sync()
		ce := d.Close()
		if e != nil || se != nil || ce != nil {
			return born, ErrHold
		}
	} else if e != nil {
		return born, ErrHold
	}
	if err := inspectOwnedGitBranch(root, born, born.Ref, uid); err != nil {
		return born, err
	}
	if _, e := root.Lstat(tmp); !os.IsNotExist(e) {
		return born, ErrHold
	}
	if afterPublish != nil {
		if err := afterPublish(); err != nil {
			return born, err
		}
	}
	if err := syncProtectedRefDirectory(root, born.Ref); err != nil {
		return born, ErrHold
	}
	born.State = "READY"
	next, _ := json.Marshal(born)
	ok, err := c.db.Replace(ownedGitBranchKey(id), raw, next)
	if err != nil || !ok {
		return born, ErrHold
	}
	return born, nil
}

func inspectOwnedGitBranch(root *os.Root, born OwnedGitBranch, path string, uid uint32) error {
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ErrHold
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 41 {
		return ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uid || st.Nlink != 1 || uint64(st.Dev) != born.Device || st.Ino != born.Inode {
		return ErrHold
	}
	raw, err := io.ReadAll(io.LimitReader(f, 42))
	if err != nil || string(raw) != born.Commit+"\n" {
		return ErrHold
	}
	public, err := root.Lstat(path)
	if err != nil || !os.SameFile(info, public) {
		return ErrHold
	}
	return nil
}
