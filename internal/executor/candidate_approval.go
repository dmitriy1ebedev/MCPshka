package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const CandidateSourceRoot = "/root/repos/mcpshka/bin"

type CandidateCopyApproval struct {
	Version                       int
	ID, CandidateID, Name, SHA256 string
	Owner                         CandidateOwner
	Source                        candidateArtifactIdentity
	Parent                        RepositoryIdentity
	ParentMount                   uint64
}

func candidateApprovalKey(id string) string {
	return "executor/candidate-copy-approvals/v1/" + id + ".json"
}

func candidateSourceName(name string) bool {
	return len(name) > len("mcpshka-.candidate") && len(name) <= 128 && filepath.Base(name) == name && strings.HasPrefix(name, "mcpshka-") && strings.HasSuffix(name, ".candidate") && !strings.ContainsAny(name, "\x00\r\n\t ")
}

// Operator-only preparation does not execute candidate bytes. A supervisor
// copy approval is neither a build attestation nor permission to deploy.
func ApproveCandidateCopy(ctx context.Context, permit, approval, candidate, name, hash string) (CandidateCopyApproval, error) {
	if os.Geteuid() != 0 {
		return CandidateCopyApproval{}, ErrHold
	}
	c, m, err := OpenPermit(permit)
	if err != nil || m.Purpose != "project-test-v1" || !validSource(m.Source) || m.Source.Repo != "/root/repos/mcpshka" {
		return CandidateCopyApproval{}, ErrHold
	}
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil || s.Result == nil {
		return CandidateCopyApproval{}, ErrHold
	}
	owner := CandidateOwner{m.Pin, s.Result.LaunchID, m.Source.Commit}
	var result CandidateCopyApproval
	err = c.WithPermitLock(ctx, m.Pin, func() error {
		var err error
		result, err = c.approveCandidateCopy(ctx, CandidateSourceRoot, approval, candidate, name, hash, owner, SystemdFence{})
		return err
	})
	if err != nil {
		return result, err
	}
	// Operator staging happens outside ProtectHome. The broker can then expose
	// the sealed private copy without any access to the legacy source tree.
	p, source, err := c.openApprovedCandidate(ctx, approval, CandidateSourceRoot)
	if err != nil {
		return result, err
	}
	defer source.Close()
	_, err = PublishCandidateImage(ctx, p.CandidateID, p.Owner.Pin, source, p.SHA256)
	return result, err
}

func (c *Controller) approveCandidateCopy(ctx context.Context, parent, id, candidate, name, hash string, owner CandidateOwner, observer stoppedObserver) (CandidateCopyApproval, error) {
	if c.db == nil || !idPattern.MatchString(id) || !idPattern.MatchString(candidate) || !candidateSourceName(name) || !digestPattern.MatchString(hash) {
		return CandidateCopyApproval{}, ErrHold
	}
	if err := c.candidateOwnerStopped(ctx, owner, observer); err != nil {
		return CandidateCopyApproval{}, err
	}
	root, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return CandidateCopyApproval{}, ErrHold
	}
	defer root.Close()
	info, err := root.Stat()
	if err != nil {
		return CandidateCopyApproval{}, ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return CandidateCopyApproval{}, ErrHold
	}
	parentID := RepositoryIdentity{CommonDir: parent, Device: uint64(st.Dev), Inode: st.Ino}
	mount, err := candidateMount(root)
	if err != nil {
		return CandidateCopyApproval{}, err
	}
	directory, err := os.OpenRoot(parent)
	if err != nil {
		return CandidateCopyApproval{}, ErrHold
	}
	defer directory.Close()
	f, err := directory.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return CandidateCopyApproval{}, ErrHold
	}
	defer f.Close()
	source, err := candidateSource(ctx, f, hash, -1)
	if err != nil || source.Mount != mount {
		return CandidateCopyApproval{}, ErrHold
	}
	current, err := directory.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		return CandidateCopyApproval{}, ErrHold
	}
	p := CandidateCopyApproval{Version: 1, ID: id, CandidateID: candidate, Name: name, SHA256: hash, Owner: owner, Source: source, Parent: parentID, ParentMount: mount}
	raw, _ := json.Marshal(p)
	fresh, err := c.db.PutIfAbsent(candidateApprovalKey(id), raw)
	if err != nil {
		return CandidateCopyApproval{}, err
	}
	if !fresh {
		prior, err := c.db.GetContext(ctx, candidateApprovalKey(id))
		var saved CandidateCopyApproval
		if err != nil || decodeCandidateRecord(prior, &saved) != nil || saved != p {
			return CandidateCopyApproval{}, ErrHold
		}
	}
	return p, nil
}

// Only an existing root-sealed approval is accepted by the daemon-facing API.
func PublishApprovedCandidate(ctx context.Context, id string) (CandidateOwnership, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) {
		return CandidateOwnership{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return CandidateOwnership{}, err
	}
	raw, err := c.db.GetContext(ctx, candidateApprovalKey(id))
	var approval CandidateCopyApproval
	if err != nil || decodeCandidateRecord(raw, &approval) != nil || approval.ID != id || approval.Version != 1 || approval.Parent.CommonDir != CandidateSourceRoot || !idPattern.MatchString(approval.CandidateID) {
		return CandidateOwnership{}, ErrHold
	}
	_, m, err := OpenPermit(approval.Owner.Pin.PermitID)
	if err != nil || m.Pin != approval.Owner.Pin || !validSource(m.Source) || m.Source.Commit != approval.Owner.PermitCommit || m.Source.Repo != "/root/repos/mcpshka" {
		return CandidateOwnership{}, ErrHold
	}
	if err := c.candidateOwnerStopped(ctx, approval.Owner, SystemdFence{}); err != nil {
		return CandidateOwnership{}, err
	}
	if prior, err := c.db.GetContext(ctx, candidateOwnershipKey(approval.CandidateID)); err == nil {
		var born CandidateOwnership
		if decodeCandidateRecord(prior, &born) != nil || born.Owner != approval.Owner || born.SHA256 != approval.SHA256 {
			return CandidateOwnership{}, ErrHold
		}
		if born.State == "READY" {
			return PublishCandidateImage(ctx, approval.CandidateID, approval.Owner.Pin, nil, approval.SHA256)
		}
	} else if !os.IsNotExist(err) {
		return CandidateOwnership{}, err
	}
	return CandidateOwnership{}, ErrHold
}

func (c *Controller) openApprovedCandidate(ctx context.Context, id, parent string) (CandidateCopyApproval, *os.File, error) {
	if c.db == nil || !idPattern.MatchString(id) {
		return CandidateCopyApproval{}, nil, ErrHold
	}
	raw, err := c.db.GetContext(ctx, candidateApprovalKey(id))
	var p CandidateCopyApproval
	if err != nil || decodeCandidateRecord(raw, &p) != nil || p.Version != 1 || p.ID != id || !idPattern.MatchString(p.CandidateID) || !validPin(p.Owner.Pin) || !idPattern.MatchString(p.Owner.LaunchID) || !commitPattern.MatchString(p.Owner.PermitCommit) || p.Parent.CommonDir != parent || !candidateSourceName(p.Name) || !digestPattern.MatchString(p.SHA256) {
		return p, nil, ErrHold
	}
	directory, err := os.OpenRoot(parent)
	if err != nil {
		return p, nil, ErrHold
	}
	defer directory.Close()
	dir, err := directory.Open(".")
	if err != nil {
		return p, nil, ErrHold
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return p, nil, ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	mount, mountErr := candidateMount(dir)
	if !ok || uint64(st.Dev) != p.Parent.Device || st.Ino != p.Parent.Inode || mountErr != nil || mount != p.ParentMount {
		return p, nil, ErrHold
	}
	f, err := directory.OpenFile(p.Name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return p, nil, ErrHold
	}
	meta, err := candidateSource(ctx, f, p.SHA256, p.Source.Size)
	if err != nil || meta != p.Source {
		f.Close()
		return p, nil, ErrHold
	}
	return p, f, nil
}
