package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const CandidateArtifactRoot = AuthorityRoot + "/candidates"

type CandidateOwner struct {
	Pin                    Pin
	LaunchID, PermitCommit string
}

type candidateArtifactIdentity struct {
	Device, Inode, Mount, Links uint64
	UID                         uint32
	Mode                        os.FileMode
	Size, Mtime, Ctime          int64
}

type CandidateOwnership struct {
	Version           int
	ID, State, SHA256 string
	Kind              string
	Size              int64
	Parent            RepositoryIdentity
	ParentMount       uint64
	Owner             CandidateOwner
	Artifact          candidateArtifactIdentity
}

func candidateOwnershipKey(id string) string {
	return "executor/candidate-ownership/v1/" + id + ".json"
}

func candidateMount(f *os.File) (uint64, error) {
	var st unix.Statx_t
	if unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &st) != nil || st.Mask&unix.STATX_MNT_ID == 0 {
		return 0, ErrHold
	}
	return st.Mnt_id, nil
}

func candidateArtifact(f *os.File) (candidateArtifactIdentity, error) {
	info, err := f.Stat()
	if err != nil {
		return candidateArtifactIdentity{}, ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || info.Size() < 0 || info.Size() > 128<<20 {
		return candidateArtifactIdentity{}, ErrHold
	}
	mount, err := candidateMount(f)
	if err != nil {
		return candidateArtifactIdentity{}, err
	}
	return candidateArtifactIdentity{uint64(st.Dev), st.Ino, mount, uint64(st.Nlink), st.Uid, info.Mode(), info.Size(), info.ModTime().UnixNano(), st.Ctim.Sec*1e9 + st.Ctim.Nsec}, nil
}

func candidateHash(ctx context.Context, f *os.File, size int64) (string, error) {
	h := sha256.New()
	buf := make([]byte, 32<<10)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		chunk := int64(len(buf))
		if chunk > size-offset {
			chunk = size - offset
		}
		n, err := f.ReadAt(buf[:chunk], offset)
		if n == 0 || err != nil && err != io.EOF {
			return "", ErrHold
		}
		h.Write(buf[:n])
		offset += int64(n)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *Controller) candidateOwnerStopped(ctx context.Context, owner CandidateOwner, observer stoppedObserver) error {
	if !validPin(owner.Pin) || !idPattern.MatchString(owner.LaunchID) || !commitPattern.MatchString(owner.PermitCommit) || observer == nil {
		return ErrHold
	}
	s, err := c.Inspect(ctx, owner.Pin)
	if err != nil || s.State != "FENCED" || len(s.Launches) != 1 || s.Result == nil || s.Result.State != "SUCCEEDED" || s.Result.LaunchID != owner.LaunchID {
		return ErrHold
	}
	l := s.Launches[0]
	if l.ID != owner.LaunchID || l.State != "FENCED" || l.Identity == nil || observer.VerifyStopped(ctx, l.ID, *l.Identity) != nil {
		return ErrHold
	}
	return nil
}

func inspectCandidateOwnership(ctx context.Context, root *os.Root, born CandidateOwnership, uid uint32) error {
	f, err := root.OpenFile(born.ID+".candidate", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ErrHold
	}
	defer f.Close()
	before, err := candidateArtifact(f)
	if err != nil || before != born.Artifact || before.UID != uid || before.Mode != 0400 || before.Mount != born.ParentMount {
		return ErrHold
	}
	hash, err := candidateHash(ctx, f, before.Size)
	if err != nil {
		return err
	}
	after, err := candidateArtifact(f)
	if err != nil || after != before || hash != born.SHA256 {
		return ErrHold
	}
	info, err := root.Lstat(born.ID + ".candidate")
	if err != nil || !info.Mode().IsRegular() {
		return ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != before.Device || st.Ino != before.Inode || st.Ctim.Sec*1e9+st.Ctim.Nsec != before.Ctime {
		return ErrHold
	}
	return nil
}

// PublishCandidateImage is a trusted supervisor boundary, not an MCP enrollment
// endpoint. This records a supervisor copy attributed to a permit, never build
// provenance or deployment authority. No legacy file is adopted.
func PublishCandidateImage(ctx context.Context, id string, pin Pin, source *os.File, digest string) (CandidateOwnership, error) {
	if os.Geteuid() != 0 {
		return CandidateOwnership{}, ErrHold
	}
	c, m, err := OpenPermit(pin.PermitID)
	if err != nil || m.Pin != pin || !validSource(m.Source) {
		return CandidateOwnership{}, ErrHold
	}
	s, err := c.Inspect(ctx, pin)
	if err != nil || s.Result == nil {
		return CandidateOwnership{}, ErrHold
	}
	owner := CandidateOwner{pin, s.Result.LaunchID, m.Source.Commit}
	var born CandidateOwnership
	err = withCandidateStoreLock(ctx, CandidateArtifactRoot, 0, func() error {
		return c.WithPermitLock(ctx, pin, func() error {
			var err error
			born, err = c.publishCandidateLocked(ctx, CandidateArtifactRoot, id, owner, source, digest, 0, SystemdFence{})
			return err
		})
	})
	return born, err
}

// Private uid/parent/observer parameters permit real-filesystem isolated tests.
func (c *Controller) publishCandidate(ctx context.Context, parent, id string, owner CandidateOwner, source *os.File, digest string, uid uint32, observer stoppedObserver) (CandidateOwnership, error) {
	var born CandidateOwnership
	err := withCandidateStoreLock(ctx, parent, uid, func() error {
		var err error
		born, err = c.publishCandidateLocked(ctx, parent, id, owner, source, digest, uid, observer)
		return err
	})
	return born, err
}

// Store lock precedes permit locks; publication and cleanup share this order.
func withCandidateStoreLock(ctx context.Context, parent string, uid uint32, fn func() error) error {
	identity, err := protectedDirectoryIdentity(parent, uid)
	if err != nil || fn == nil {
		return ErrHold
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return ErrHold
	}
	defer root.Close()
	parentInfo, err := root.Stat(".")
	if err != nil {
		return ErrHold
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(parentStat.Dev) != identity.Device || parentStat.Ino != identity.Inode {
		return ErrHold
	}
	f, err := root.OpenFile(".store.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return ErrHold
	}
	defer f.Close()
	var st syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &st) != nil || st.Uid != uid || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 {
		return ErrHold
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return ErrHold
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	current, err := protectedDirectoryIdentity(parent, uid)
	info, statErr := root.Lstat(".store.lock")
	if err != nil || current != identity || statErr != nil {
		return ErrHold
	}
	pathStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(pathStat.Dev) != uint64(st.Dev) || pathStat.Ino != st.Ino {
		return ErrHold
	}
	return fn()
}

func (c *Controller) publishCandidateLocked(ctx context.Context, parent, id string, owner CandidateOwner, source *os.File, digest string, uid uint32, observer stoppedObserver) (CandidateOwnership, error) {
	if err := ctx.Err(); err != nil {
		return CandidateOwnership{}, err
	}
	if c.db == nil || !idPattern.MatchString(id) || !digestPattern.MatchString(digest) {
		return CandidateOwnership{}, ErrHold
	}
	if err := c.candidateOwnerStopped(ctx, owner, observer); err != nil {
		return CandidateOwnership{}, err
	}
	parentIdentity, err := protectedDirectoryIdentity(parent, uid)
	if err != nil {
		return CandidateOwnership{}, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return CandidateOwnership{}, ErrHold
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return CandidateOwnership{}, ErrHold
	}
	defer dir.Close()
	parentMount, err := candidateMount(dir)
	if err != nil {
		return CandidateOwnership{}, err
	}
	pinned, err := dir.Stat()
	if err != nil {
		return CandidateOwnership{}, ErrHold
	}
	pst, ok := pinned.Sys().(*syscall.Stat_t)
	if !ok || uint64(pst.Dev) != parentIdentity.Device || pst.Ino != parentIdentity.Inode {
		return CandidateOwnership{}, ErrHold
	}
	intent := CandidateOwnership{Version: 1, ID: id, State: "CREATE_INTENT", Kind: "supervisor-copy-v1", SHA256: digest, Parent: parentIdentity, ParentMount: parentMount, Owner: owner}
	raw, err := c.db.GetContext(ctx, candidateOwnershipKey(id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CandidateOwnership{}, err
	}
	var created CandidateOwnership
	if err == nil {
		if len(raw) > 4096 {
			return CandidateOwnership{}, ErrHold
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&created) != nil || dec.Decode(new(any)) != io.EOF || created.Size < 0 || created.Size > 128<<20 {
			return CandidateOwnership{}, ErrHold
		}
		intent.Size = created.Size
		comparison := created
		comparison.State = "CREATE_INTENT"
		comparison.Artifact = candidateArtifactIdentity{}
		if comparison != intent {
			return CandidateOwnership{}, ErrHold
		}
		if created.State == "READY" {
			if err := inspectCandidateOwnership(ctx, root, created, uid); err != nil {
				return CandidateOwnership{}, err
			}
			if err := revalidateCandidateParent(parent, parentIdentity, parentMount, uid); err != nil {
				return CandidateOwnership{}, err
			}
			return created, nil
		}
		if created.State != "CREATE_INTENT" && created.State != "CREATED" {
			return CandidateOwnership{}, ErrHold
		}
		if created.State == "CREATE_INTENT" && created.Artifact != (candidateArtifactIdentity{}) {
			return CandidateOwnership{}, ErrHold
		}
	} else {
		before, err := candidateSource(ctx, source, digest, -1)
		if err != nil {
			return CandidateOwnership{}, err
		}
		intent.Size = before.Size
		raw, _ = json.Marshal(intent)
		fresh, err := c.db.PutIfAbsent(candidateOwnershipKey(id), raw)
		if err != nil {
			return CandidateOwnership{}, err
		}
		if !fresh {
			return CandidateOwnership{}, ErrHold
		}
		created = intent
	}
	name := id + ".candidate"
	var f *os.File
	if created.State == "CREATE_INTENT" {
		before, err := candidateSource(ctx, source, digest, intent.Size)
		if err != nil {
			return CandidateOwnership{}, err
		}
		if err := protectedImportSpace(root, uint64(before.Size)); err != nil {
			return CandidateOwnership{}, err
		}
		if err := ctx.Err(); err != nil {
			return CandidateOwnership{}, err
		}
		f, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return CandidateOwnership{}, ErrHold
		}
		defer f.Close()
		// A create-before-record crash stays UNKNOWN; never adopt that pathname.
		identity, err := candidateArtifact(f)
		if err != nil || identity.UID != uid || identity.Mount != parentMount || f.Sync() != nil || dir.Sync() != nil {
			return CandidateOwnership{}, ErrHold
		}
		created.State = "CREATED"
		created.Artifact = identity
		next, _ := json.Marshal(created)
		ok, err := c.db.Replace(candidateOwnershipKey(id), raw, next)
		if err != nil || !ok {
			return CandidateOwnership{}, ErrHold
		}
		raw = next
	} else {
		f, err = root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return CandidateOwnership{}, ErrHold
		}
		defer f.Close()
		current, err := candidateArtifact(f)
		if err != nil || !sameCreatedCandidate(current, created.Artifact, uid, parentMount) {
			return CandidateOwnership{}, ErrHold
		}
		if current.Size == intent.Size {
			hash, err := candidateHash(ctx, f, current.Size)
			if err != nil {
				return CandidateOwnership{}, err
			}
			after, err := candidateArtifact(f)
			if err != nil || after != current {
				return CandidateOwnership{}, ErrHold
			}
			if hash == digest {
				return c.finishCandidatePublication(ctx, parent, root, dir, f, created, raw, uid)
			}
		}
		if current.Mode.Perm() != 0600 {
			return CandidateOwnership{}, ErrHold
		}
		writable, err := root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return CandidateOwnership{}, ErrHold
		}
		defer writable.Close()
		opened, err := candidateArtifact(writable)
		if err != nil || opened != current {
			return CandidateOwnership{}, ErrHold
		}
		f = writable
	}
	before, err := candidateSource(ctx, source, digest, intent.Size)
	if err != nil {
		return CandidateOwnership{}, err
	}
	target, err := candidateArtifact(f)
	if err != nil || !sameCreatedCandidate(target, created.Artifact, uid, parentMount) || target.Mode.Perm() != 0600 || before.Device == target.Device && before.Inode == target.Inode {
		return CandidateOwnership{}, ErrHold
	}
	if err := protectedImportSpace(root, uint64(before.Size)); err != nil {
		return CandidateOwnership{}, err
	}
	if err := ctx.Err(); err != nil {
		return CandidateOwnership{}, err
	}
	if err := f.Truncate(0); err != nil {
		return CandidateOwnership{}, ErrHold
	}
	buf := make([]byte, 32<<10)
	for offset := int64(0); offset < before.Size; {
		if err := ctx.Err(); err != nil {
			return CandidateOwnership{}, err
		}
		chunk := int64(len(buf))
		if chunk > before.Size-offset {
			chunk = before.Size - offset
		}
		n, err := source.ReadAt(buf[:chunk], offset)
		if n == 0 || err != nil && err != io.EOF {
			return CandidateOwnership{}, ErrHold
		}
		if written, err := f.WriteAt(buf[:n], offset); err != nil || written != n {
			return CandidateOwnership{}, ErrHold
		}
		offset += int64(n)
	}
	after, err := candidateArtifact(source)
	if err != nil || after != before {
		return CandidateOwnership{}, ErrHold
	}
	return c.finishCandidatePublication(ctx, parent, root, dir, f, created, raw, uid)
}

func candidateSource(ctx context.Context, source *os.File, digest string, size int64) (candidateArtifactIdentity, error) {
	if source == nil {
		return candidateArtifactIdentity{}, ErrHold
	}
	before, err := candidateArtifact(source)
	if err != nil || size >= 0 && before.Size != size {
		return candidateArtifactIdentity{}, ErrHold
	}
	hash, err := candidateHash(ctx, source, before.Size)
	if err != nil {
		return candidateArtifactIdentity{}, err
	}
	after, err := candidateArtifact(source)
	if err != nil || after != before || hash != digest {
		return candidateArtifactIdentity{}, ErrHold
	}
	return before, nil
}

func sameCreatedCandidate(current, created candidateArtifactIdentity, uid uint32, mount uint64) bool {
	return created.Inode != 0 && current.Device == created.Device && current.Inode == created.Inode && current.Mount == created.Mount && current.Mount == mount && current.UID == uid && created.UID == uid && current.Links == 1 && created.Links == 1 && (current.Mode == 0600 || current.Mode == 0400)
}

func revalidateCandidateParent(parent string, expected RepositoryIdentity, mount uint64, uid uint32) error {
	current, err := protectedDirectoryIdentity(parent, uid)
	if err != nil || current != expected {
		return ErrHold
	}
	dir, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrHold
	}
	defer dir.Close()
	st, err := dir.Stat()
	if err != nil {
		return ErrHold
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || uint64(meta.Dev) != expected.Device || meta.Ino != expected.Inode {
		return ErrHold
	}
	observed, err := candidateMount(dir)
	if err != nil || observed != mount {
		return ErrHold
	}
	return nil
}

func (c *Controller) finishCandidatePublication(ctx context.Context, parent string, root *os.Root, dir, f *os.File, created CandidateOwnership, raw []byte, uid uint32) (CandidateOwnership, error) {
	artifact, err := candidateArtifact(f)
	if err != nil || !sameCreatedCandidate(artifact, created.Artifact, uid, created.ParentMount) || artifact.Size != created.Size {
		return CandidateOwnership{}, ErrHold
	}
	if artifact.Mode.Perm() != 0400 {
		if err := f.Chmod(0400); err != nil {
			return CandidateOwnership{}, ErrHold
		}
	}
	if f.Sync() != nil || dir.Sync() != nil {
		return CandidateOwnership{}, ErrHold
	}
	artifact, err = candidateArtifact(f)
	if err != nil {
		return CandidateOwnership{}, err
	}
	born := created
	born.State = "READY"
	born.Artifact = artifact
	if err := inspectCandidateOwnership(ctx, root, born, uid); err != nil {
		return CandidateOwnership{}, err
	}
	if err := revalidateCandidateParent(parent, born.Parent, born.ParentMount, uid); err != nil {
		return CandidateOwnership{}, err
	}
	if err := ctx.Err(); err != nil {
		return CandidateOwnership{}, err
	}
	ready, _ := json.Marshal(born)
	ok, err := c.db.Replace(candidateOwnershipKey(born.ID), raw, ready)
	if err != nil || !ok {
		return CandidateOwnership{}, ErrHold
	}
	return born, nil
}
