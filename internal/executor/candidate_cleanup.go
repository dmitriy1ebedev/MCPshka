package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mcpshka/internal/failure"
	"os"
	"syscall"
	"time"
)

type CandidateRetirement struct {
	Version            int
	State, Fingerprint string
	Candidate          CandidateOwnership
	Protected          [2]string
}

const CandidateDeploymentLock = "/run/mcpshka-self-deploy.lock"

// Fixed root-only maintenance boundary. No caller path or ownership claim is
// accepted; a fingerprint authorizes only this already-owned image.
func RetireCandidateImage(ctx context.Context, id, fingerprint string) (CandidateRetirement, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) {
		return CandidateRetirement{}, Hold(failure.PolicyDenied, "RETIRE_INPUT_INVALID", "candidate.RetireCandidateImage", "retirement requires root and candidate id", nil)
	}
	c, err := OpenAuthority()
	if err != nil {
		return CandidateRetirement{}, err
	}
	var result CandidateRetirement
	err = withCandidateDeploymentLock(ctx, func() error {
		return withCandidateStoreLock(ctx, CandidateArtifactRoot, 0, func() error {
			raw, err := c.db.GetContext(ctx, candidateOwnershipKey(id))
			var born CandidateOwnership
			if err != nil || decodeCandidateRecord(raw, &born) != nil {
				return Hold(failure.FencedUnknown, "CANDIDATE_RECORD_UNAVAILABLE", "candidate.RetireCandidateImage", "candidate ownership record unavailable", err)
			}
			invoke := func() error {
				var err error
				result, err = c.retireCandidateLocked(ctx, CandidateArtifactRoot, id, fingerprint, [2]string{"/usr/local/bin/mcpshka", "/usr/local/bin/mcpshka.previous"}, 0, SystemdFence{})
				return err
			}
			root, err := os.OpenRoot(CandidateArtifactRoot)
			if err != nil {
				return Hold(failure.EvidenceMissing, "CANDIDATE_STORE_UNAVAILABLE", "candidate.RetireCandidateImage", "candidate store unavailable", err)
			}
			_, pathErr := root.Lstat(id + ".candidate")
			root.Close()
			if errors.Is(pathErr, os.ErrNotExist) {
				return invoke()
			}
			return c.WithPermitLock(ctx, born.Owner.Pin, invoke)
		})
	})
	return result, err
}

func withCandidateDeploymentLock(ctx context.Context, fn func() error) error {
	started := time.Now()
	if ctx.Err() != nil {
		return contextFailure(ctx, "candidate.withCandidateDeploymentLock", started, ctx.Err())
	}
	parent, err := os.Lstat("/run")
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0022 != 0 {
		return Hold(failure.PolicyDenied, "DEPLOY_LOCK_PARENT_UNSAFE", "candidate.withCandidateDeploymentLock", "deployment lock parent unsafe", err)
	}
	pst, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || pst.Uid != 0 {
		return Hold(failure.PolicyDenied, "DEPLOY_LOCK_PARENT_OWNER", "candidate.withCandidateDeploymentLock", "deployment lock parent owner invalid", nil)
	}
	fd, err := syscall.Open(CandidateDeploymentLock, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Hold(failure.EvidenceMissing, "DEPLOY_LOCK_MISSING", "candidate.withCandidateDeploymentLock", "deployment lock unavailable", err)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 {
		return Hold(failure.PolicyDenied, "DEPLOY_LOCK_UNSAFE", "candidate.withCandidateDeploymentLock", "deployment lock owner, type, links or mode invalid", nil)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return Hold(failure.Transient, "DEPLOY_LOCK_FAILED", "candidate.withCandidateDeploymentLock", "deployment lock failed", err)
		}
		return Hold(failure.OwnerBusy, "DEPLOY_LOCK_BUSY", "candidate.withCandidateDeploymentLock", "deployment lock held by another operation", err)
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	current, err := os.Lstat(CandidateDeploymentLock)
	if err != nil || !current.Mode().IsRegular() {
		return Hold(failure.FencedUnknown, "DEPLOY_LOCK_PATH_CHANGED", "candidate.withCandidateDeploymentLock", "deployment lock path changed", err)
	}
	cst, ok := current.Sys().(*syscall.Stat_t)
	if !ok || cst.Dev != st.Dev || cst.Ino != st.Ino {
		return Hold(failure.FencedUnknown, "DEPLOY_LOCK_IDENTITY_CHANGED", "candidate.withCandidateDeploymentLock", "deployment lock inode changed", nil)
	}
	return fn()
}

func candidateRetirementKey(id string) string {
	return "executor/candidate-retirements/v1/" + id + ".json"
}

func decodeCandidateRecord(raw []byte, out any) error {
	if len(raw) > 8192 {
		return Hold(failure.PolicyDenied, "CANDIDATE_RECORD_LIMIT", "candidate.decodeCandidateRecord", "candidate record too large", nil)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return Hold(failure.FencedUnknown, "CANDIDATE_RECORD_INVALID", "candidate.decodeCandidateRecord", "candidate record invalid or has extra fields", nil)
	}
	return nil
}

func candidateRetirementFingerprint(p CandidateRetirement) string {
	p.State, p.Fingerprint = "", ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func protectedImageHash(ctx context.Context, path string, uid uint32) (string, error) {
	started := time.Now()
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", Hold(failure.EvidenceMissing, "PROTECTED_IMAGE_UNAVAILABLE", "candidate.protectedImageHash", "protected image unavailable", err)
	}
	defer f.Close()
	before, err := candidateArtifact(f)
	if err != nil || before.UID != uid {
		return "", Hold(failure.PolicyDenied, "PROTECTED_IMAGE_OWNER", "candidate.protectedImageHash", "protected image metadata or owner invalid", err)
	}
	hash, err := candidateHash(ctx, f, before.Size)
	if err != nil {
		return "", contextFailure(ctx, "candidate.protectedImageHash", started, err)
	}
	after, err := candidateArtifact(f)
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || before != after || pathErr != nil || !pathInfo.Mode().IsRegular() {
		return "", Hold(failure.FencedUnknown, "PROTECTED_IMAGE_CHANGED", "candidate.protectedImageHash", "protected image changed during hashing", err)
	}
	st, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != before.Device || st.Ino != before.Inode || st.Ctim.Sec*1e9+st.Ctim.Nsec != before.Ctime {
		return "", Hold(failure.FencedUnknown, "PROTECTED_IMAGE_IDENTITY", "candidate.protectedImageHash", "protected image inode or ctime changed", nil)
	}
	return hash, nil
}

// The caller holds the deployment lock and store lock, in that order. Public
// entry points fix protected paths; worker-supplied hashes cannot protect files.
func (c *Controller) retireCandidateLocked(ctx context.Context, parent, id, fingerprint string, protected [2]string, uid uint32, observer stoppedObserver) (CandidateRetirement, error) {
	started := time.Now()
	if c.db == nil || !idPattern.MatchString(id) || fingerprint != "" && !digestPattern.MatchString(fingerprint) {
		return CandidateRetirement{}, Hold(failure.PolicyDenied, "RETIRE_PIN_INVALID", "candidate.retireCandidateLocked", "retirement database, id or fingerprint invalid", nil)
	}
	raw, err := c.db.GetContext(ctx, candidateOwnershipKey(id))
	var born CandidateOwnership
	if err != nil || decodeCandidateRecord(raw, &born) != nil || born.Version != 1 || born.ID != id || born.State != "READY" || born.Kind != "supervisor-copy-v1" || !digestPattern.MatchString(born.SHA256) || born.Parent.CommonDir != parent || born.Size != born.Artifact.Size || born.Artifact.Mode != 0400 || born.Artifact.UID != uid || born.Artifact.Links != 1 {
		return CandidateRetirement{}, Hold(failure.FencedUnknown, "CANDIDATE_OWNERSHIP_INVALID", "candidate.retireCandidateLocked", "candidate ownership binding invalid", err)
	}
	if err := revalidateCandidateParent(parent, born.Parent, born.ParentMount, uid); err != nil {
		return CandidateRetirement{}, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return CandidateRetirement{}, Hold(failure.EvidenceMissing, "CANDIDATE_PARENT_UNAVAILABLE", "candidate.retireCandidateLocked", "candidate parent unavailable", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return CandidateRetirement{}, Hold(failure.EvidenceMissing, "CANDIDATE_DIRECTORY_UNAVAILABLE", "candidate.retireCandidateLocked", "candidate directory unavailable", err)
	}
	defer dir.Close()
	key := candidateRetirementKey(id)
	prior, priorErr := c.db.GetContext(ctx, key)
	var saved CandidateRetirement
	if priorErr == nil {
		if decodeCandidateRecord(prior, &saved) != nil || saved.Version != 1 || saved.Candidate != born || !digestPattern.MatchString(saved.Fingerprint) || candidateRetirementFingerprint(saved) != saved.Fingerprint || saved.State != "DELETE_INTENT" && saved.State != "DELETED" {
			return CandidateRetirement{}, Hold(failure.FencedUnknown, "RETIRE_RECORD_INVALID", "candidate.retireCandidateLocked", "prior retirement receipt invalid", nil)
		}
	} else if !errors.Is(priorErr, os.ErrNotExist) {
		return CandidateRetirement{}, priorErr
	}
	_, pathErr := root.Lstat(id + ".candidate")
	if errors.Is(pathErr, os.ErrNotExist) && priorErr == nil {
		// Reconciling absence performs no deletion and needs no fresh owner or
		// currently installed image. The sealed operation is the recovery pin.
		if fingerprint == "" {
			return saved, nil
		}
		if fingerprint != saved.Fingerprint || dir.Sync() != nil {
			return saved, Hold(failure.FencedUnknown, "RETIRE_RECONCILE_FAILED", "candidate.retireCandidateLocked", "retirement fingerprint or directory sync unverified", nil)
		}
		return c.finishCandidateRetirement(key, prior, saved)
	}
	if pathErr != nil || priorErr == nil && saved.State == "DELETED" {
		return saved, Hold(failure.FencedUnknown, "CANDIDATE_PATH_CONFLICT", "candidate.retireCandidateLocked", "candidate path conflicts with prior deletion", pathErr)
	}
	if err := c.candidateOwnerStopped(ctx, born.Owner, observer); err != nil {
		return CandidateRetirement{}, err
	}
	plan := CandidateRetirement{Version: 1, State: "PLANNED", Candidate: born}
	for i, path := range protected {
		plan.Protected[i], err = protectedImageHash(ctx, path, uid)
		if err != nil {
			return CandidateRetirement{}, err
		}
		if plan.Protected[i] == born.SHA256 {
			return CandidateRetirement{}, Hold(failure.PolicyDenied, "PROTECTED_IMAGE_RETAINED", "candidate.retireCandidateLocked", "candidate is a protected installed image", nil)
		}
	}
	plan.Fingerprint = candidateRetirementFingerprint(plan)
	if priorErr == nil {
		if saved.Fingerprint != plan.Fingerprint {
			return plan, Hold(failure.FencedUnknown, "RETIRE_PLAN_CHANGED", "candidate.retireCandidateLocked", "retirement plan differs from saved intent", nil)
		}
	}
	if err := inspectCandidateOwnership(ctx, root, born, uid); err != nil {
		return plan, err
	}
	if fingerprint == "" {
		return plan, nil
	}
	if fingerprint != plan.Fingerprint {
		return plan, Hold(failure.FencedUnknown, "RETIRE_FINGERPRINT_MISMATCH", "candidate.retireCandidateLocked", "retirement fingerprint does not match plan", nil)
	}
	if priorErr != nil {
		plan.State = "DELETE_INTENT"
		prior, _ = json.Marshal(plan)
		ok, err := c.db.PutIfAbsent(key, prior)
		if err != nil || !ok {
			return plan, Hold(failure.FencedUnknown, "RETIRE_INTENT_CONFLICT", "candidate.retireCandidateLocked", "retirement intent could not be recorded", err)
		}
		saved = plan
	}
	// Private root-born storage excludes worker descriptors; the common store
	// lock excludes supervised publishers/readers until descriptor-relative unlink.
	if err := ctx.Err(); err != nil {
		return plan, contextFailure(ctx, "candidate.retireCandidateLocked", started, err)
	}
	if err := c.candidateOwnerStopped(ctx, born.Owner, observer); err != nil {
		return plan, err
	}
	if err := revalidateCandidateParent(parent, born.Parent, born.ParentMount, uid); err != nil {
		return plan, err
	}
	if err := inspectCandidateOwnership(ctx, root, born, uid); err != nil {
		return plan, err
	}
	if root.Remove(id+".candidate") != nil || dir.Sync() != nil {
		return plan, Hold(failure.FencedUnknown, "RETIRE_UNLINK_UNVERIFIED", "candidate.retireCandidateLocked", "candidate unlink or sync unverified", nil)
	}
	return c.finishCandidateRetirement(key, prior, saved)
}

func (c *Controller) finishCandidateRetirement(key string, raw []byte, saved CandidateRetirement) (CandidateRetirement, error) {
	if saved.State == "DELETED" {
		return saved, nil
	}
	saved.State = "DELETED"
	next, _ := json.Marshal(saved)
	ok, err := c.db.Replace(key, raw, next)
	if err != nil || !ok {
		return saved, Hold(failure.FencedUnknown, "RETIRE_COMMIT_CONFLICT", "candidate.finishCandidateRetirement", "retirement receipt could not be committed", err)
	}
	return saved, nil
}
