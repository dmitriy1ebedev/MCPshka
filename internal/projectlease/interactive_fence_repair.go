package projectlease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type InteractiveFenceRepairPlan struct {
	Path            string `json:"path"`
	DirectoryDevice uint64 `json:"directory_device"`
	DirectoryInode  uint64 `json:"directory_inode"`
	FenceDevice     uint64 `json:"fence_device"`
	FenceInode      uint64 `json:"fence_inode"`
	BeforeUID       uint32 `json:"before_uid"`
	BeforeGID       uint32 `json:"before_gid"`
	TargetUID       uint32 `json:"target_uid"`
	TargetGID       uint32 `json:"target_gid"`
	JournalID       string `json:"journal_id"`
	Authorization   string `json:"authorization"`
}

type InteractiveFenceRepairResult struct {
	State       string `json:"state"`
	Path        string `json:"path"`
	JournalID   string `json:"journal_id"`
	FenceDevice uint64 `json:"fence_device"`
	FenceInode  uint64 `json:"fence_inode"`
	UID         uint32 `json:"uid"`
	GID         uint32 `json:"gid"`
}

type interactiveFenceRepairJournal struct {
	Schema string                       `json:"schema"`
	At     time.Time                    `json:"at"`
	Plan   InteractiveFenceRepairPlan   `json:"plan"`
	Result InteractiveFenceRepairResult `json:"result"`
}

func interactiveFenceRepairID(p InteractiveFenceRepairPlan) string {
	p.JournalID, p.Authorization = "", ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) interactiveFenceRepairScope(path string) error {
	if os.Geteuid() != 0 || s == nil || s.database == nil || s.stateErr != nil {
		return errors.New("trusted root SQLite operator required")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("noncanonical project scope")
	}
	root, err := s.projectRoot(path)
	if err != nil || root != path {
		return errors.New("project scope differs")
	}
	return nil
}

// The named inode is never created, replaced, removed or chmodded by repair.
func (s *Service) openRepairFence(targetUID, targetGID uint32, path string) (*os.File, *os.File, error) {
	if targetUID == 0 || !filepath.IsAbs(s.dir) || filepath.Clean(s.dir) != s.dir {
		return nil, nil, errors.New("invalid service directory identity")
	}
	real, err := filepath.EvalSymlinks(s.dir)
	if err != nil || real != s.dir {
		return nil, nil, errors.New("lease directory alias or unavailable")
	}
	dfd, err := syscall.Open(s.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	directory := os.NewFile(uintptr(dfd), s.dir)
	info, err := directory.Stat()
	var identity *syscall.Stat_t
	if err == nil {
		identity, _ = info.Sys().(*syscall.Stat_t)
	}
	if err != nil || identity == nil || info.Mode() != os.ModeDir|0700 || identity.Uid != targetUID || identity.Gid != targetGID {
		directory.Close()
		return nil, nil, errors.New("lease directory is not private service authority")
	}
	name := s.leasePath(path) + ".effect.lock"
	fd, err := syscall.Openat(dfd, filepath.Base(name), syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		directory.Close()
		return nil, nil, err
	}
	fence := os.NewFile(uintptr(fd), name)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fence.Close()
		directory.Close()
		return nil, nil, errors.New("interactive fence busy")
	}
	return directory, fence, nil
}

func (s *Service) repairFenceIdentity(directory, fence *os.File, p InteractiveFenceRepairPlan) (*syscall.Stat_t, error) {
	dir, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	dst, ok := dir.Sys().(*syscall.Stat_t)
	linkedDir, err := os.Lstat(s.dir)
	if !ok || err != nil || !os.SameFile(dir, linkedDir) || dir.Mode() != os.ModeDir|0700 || dst.Uid != p.TargetUID || dst.Gid != p.TargetGID || uint64(dst.Dev) != p.DirectoryDevice || dst.Ino != p.DirectoryInode {
		return nil, errors.New("lease directory identity changed")
	}
	info, err := fence.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	linked, err := os.Lstat(fence.Name())
	if !ok || err != nil || !os.SameFile(info, linked) || info.Mode() != 0600 || info.Size() != 0 || st.Nlink != 1 || uint64(st.Dev) != p.FenceDevice || st.Ino != p.FenceInode {
		return nil, errors.New("interactive fence identity changed or unsafe")
	}
	if (st.Uid != p.BeforeUID || st.Gid != p.BeforeGID) && (st.Uid != p.TargetUID || st.Gid != p.TargetGID) {
		return nil, errors.New("interactive fence owner changed")
	}
	return st, nil
}

func (s *Service) PreviewInteractiveFenceRepair(path string, targetUID, targetGID uint32) (InteractiveFenceRepairPlan, error) {
	if err := s.interactiveFenceRepairScope(path); err != nil {
		return InteractiveFenceRepairPlan{}, err
	}
	if err := s.writerAdmission(path); err != nil {
		return InteractiveFenceRepairPlan{}, err
	}
	// writerAdmission trusts root; a fresh plan must still see any writer gate.
	if _, err := s.database.Get(s.writerGateKey(path)); !errors.Is(err, os.ErrNotExist) {
		return InteractiveFenceRepairPlan{}, errors.New("writer gate held or authority unavailable")
	}
	directory, fence, err := s.openRepairFence(targetUID, targetGID, path)
	if err != nil {
		return InteractiveFenceRepairPlan{}, err
	}
	defer directory.Close()
	defer fence.Close()
	dir, err := directory.Stat()
	if err != nil {
		return InteractiveFenceRepairPlan{}, err
	}
	info, err := fence.Stat()
	if err != nil {
		return InteractiveFenceRepairPlan{}, err
	}
	dst, dok := dir.Sys().(*syscall.Stat_t)
	st, sok := info.Sys().(*syscall.Stat_t)
	if !dok || !sok || st.Uid != 0 {
		return InteractiveFenceRepairPlan{}, errors.New("expected an existing root-owned interactive fence")
	}
	p := InteractiveFenceRepairPlan{Path: path, DirectoryDevice: uint64(dst.Dev), DirectoryInode: dst.Ino, FenceDevice: uint64(st.Dev), FenceInode: st.Ino, BeforeUID: st.Uid, BeforeGID: st.Gid, TargetUID: targetUID, TargetGID: targetGID}
	if _, err := s.repairFenceIdentity(directory, fence, p); err != nil {
		return InteractiveFenceRepairPlan{}, err
	}
	if _, err := s.database.Get("leases/" + filepath.Base(s.leasePath(path))); !errors.Is(err, os.ErrNotExist) {
		return InteractiveFenceRepairPlan{}, errors.New("lease held or authority unavailable")
	}
	p.JournalID = interactiveFenceRepairID(p)
	return p, nil
}

// Jobs/deployment admission and the active administrative grant remain held by
// the trusted caller. A partial repair keeps its pinned writer gate for resume.
func (s *Service) RepairInteractiveFence(p InteractiveFenceRepairPlan) (InteractiveFenceRepairResult, error) {
	hold := func(err error) (InteractiveFenceRepairResult, error) {
		return InteractiveFenceRepairResult{}, fmt.Errorf("interactive fence repair HOLD: %w", err)
	}
	if err := s.interactiveFenceRepairScope(p.Path); err != nil {
		return hold(err)
	}
	if p.BeforeUID != 0 || p.TargetUID == 0 || strings.TrimSpace(p.Authorization) == "" || len(p.Authorization) > 1024 || p.JournalID != interactiveFenceRepairID(p) {
		return hold(errors.New("invalid pinned administrative plan"))
	}
	directory, fence, err := s.openRepairFence(p.TargetUID, p.TargetGID, p.Path)
	if err != nil {
		return hold(err)
	}
	defer directory.Close()
	defer fence.Close()
	st, err := s.repairFenceIdentity(directory, fence, p)
	if err != nil {
		return hold(err)
	}
	result := InteractiveFenceRepairResult{State: "repaired", Path: p.Path, JournalID: p.JournalID, FenceDevice: p.FenceDevice, FenceInode: p.FenceInode, UID: p.TargetUID, GID: p.TargetGID}
	journalKey := "project-fence-repairs/v1/" + p.JournalID
	raw, err := s.database.Get(journalKey)
	if err == nil {
		var journal interactiveFenceRepairJournal
		if json.Unmarshal(raw, &journal) != nil || journal.Schema != "mcpshka.interactive-fence-repair/v1" || journal.Plan != p || journal.Result != result || journal.At.IsZero() || st.Uid != p.TargetUID || st.Gid != p.TargetGID {
			return hold(errors.New("completed repair proof or current inode differs"))
		}
		st, err = s.repairFenceIdentity(directory, fence, p)
		if err != nil || st.Uid != p.TargetUID || st.Gid != p.TargetGID {
			return hold(errors.New("completed repair inode changed during reconciliation"))
		}
		return result, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return hold(err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return hold(err)
	}
	gate := s.writerGateKey(p.Path)
	existing, err := s.database.Get(gate)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return hold(err)
	}
	if err == nil && !bytes.Equal(existing, body) {
		return hold(errors.New("foreign writer gate"))
	}
	if errors.Is(err, os.ErrNotExist) && (st.Uid != p.BeforeUID || st.Gid != p.BeforeGID) {
		return hold(errors.New("changed owner without a pending repair gate"))
	}
	leaseKey := "leases/" + filepath.Base(s.leasePath(p.Path))
	created, err := s.database.PutIfAbsentUnlessGate(gate, body, leaseKey)
	if err != nil {
		return hold(err)
	}
	if !created {
		existing, err := s.database.Get(gate)
		if err != nil || !bytes.Equal(existing, body) {
			return hold(errors.New("lease held or writer gate changed"))
		}
	}
	if _, err := s.database.Get(leaseKey); !errors.Is(err, os.ErrNotExist) {
		return hold(errors.New("lease held or authority unavailable"))
	}
	if err := noSealedProcessReferences([]string{p.Path}); err != nil {
		return hold(err)
	}
	st, err = s.repairFenceIdentity(directory, fence, p)
	if err != nil {
		return hold(err)
	}
	if st.Uid != p.TargetUID || st.Gid != p.TargetGID {
		if err := fence.Chown(int(p.TargetUID), int(p.TargetGID)); err != nil {
			return hold(err)
		}
	}
	if err := fence.Sync(); err != nil {
		return hold(err)
	}
	st, err = s.repairFenceIdentity(directory, fence, p)
	if err != nil || st.Uid != p.TargetUID || st.Gid != p.TargetGID {
		return hold(errors.New("repaired inode proof changed; retain gate"))
	}
	journal, err := json.Marshal(interactiveFenceRepairJournal{Schema: "mcpshka.interactive-fence-repair/v1", At: time.Now().UTC(), Plan: p, Result: result})
	if err != nil {
		return hold(err)
	}
	done, err := s.database.DeleteWithJournal(gate, body, journalKey, journal)
	if err != nil || !done {
		return hold(errors.New("completion journal outcome UNKNOWN; inspect exact gate and inode"))
	}
	return result, nil
}
