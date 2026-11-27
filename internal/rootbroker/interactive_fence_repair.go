package rootbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
)

type InteractiveFencePreview struct {
	State      string                                   `json:"state"`
	Plan       *projectlease.InteractiveFenceRepairPlan `json:"plan,omitempty"`
	FenceInode uint64                                   `json:"fence_inode,omitempty"`
}

func healthyInteractiveFence(path string, uid, gid uint32) (InteractiveFencePreview, error) {
	const dir = "/var/lib/mcpshka/leases"
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return InteractiveFencePreview{}, errors.New("lease directory unavailable")
	}
	d, err := os.Lstat(dir)
	if err != nil {
		return InteractiveFencePreview{}, err
	}
	ds, ok := d.Sys().(*syscall.Stat_t)
	if !ok || !d.IsDir() || d.Mode().Perm() != 0700 || ds.Uid != uid || ds.Gid != gid {
		return InteractiveFencePreview{}, errors.New("untrusted lease directory")
	}
	f, err := os.Lstat(filepath.Join(dir, shaHex([]byte(path))[:32]+".json.effect.lock"))
	if err != nil {
		return InteractiveFencePreview{}, err
	}
	st, ok := f.Sys().(*syscall.Stat_t)
	if !ok || !f.Mode().IsRegular() || f.Mode().Perm() != 0600 || f.Size() != 0 || st.Nlink != 1 || st.Uid != uid || st.Gid != gid {
		return InteractiveFencePreview{}, errors.New("interactive fence is not service-owned")
	}
	return InteractiveFencePreview{State: "service_owned", FenceInode: st.Ino}, nil
}

// fenceRepairRepository admits any immediate repository under /root/repos; the
// plan still pins directory and fence inodes and requires an idle project.
// The root-only protected integration store is never a writer project.
func fenceRepairRepository(path string) bool {
	return recipeRepository.MatchString(path) && filepath.Clean(path) == path && path != "/root/repos/mcpshka-controlled"
}

func decodeFenceRepairPlan(raw string) (projectlease.InteractiveFenceRepairPlan, error) {
	var p projectlease.InteractiveFenceRepairPlan
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if len(raw) > 4096 || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !fenceRepairRepository(p.Path) || p.Authorization != "" || p.BeforeUID != 0 || p.TargetUID != 994 || !validHash(p.JournalID) {
		return p, errors.New("invalid fixed interactive fence repair plan")
	}
	return p, nil
}

func fenceRepairTarget() (uint32, uint32, error) {
	u, err := user.Lookup("mcpshka")
	if err != nil || u.Uid != "994" {
		return 0, 0, errors.New("installed service identity unavailable")
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil || gid == 0 {
		return 0, 0, errors.New("installed service group unavailable")
	}
	return 994, uint32(gid), nil
}

func adminInteractiveFence(args []string, now time.Time) (any, error) {
	if err := validateLegacyLeaseRequest(args); err != nil {
		return nil, err
	}
	lock, session, err := adminLegacyLeaseSession(args, now)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	uid, gid, err := fenceRepairTarget()
	if err != nil {
		return nil, err
	}
	s := projectlease.New("/var/lib/mcpshka/leases", policy.Default())
	if args[0] == "preview-interactive-fence" {
		p, err := s.PreviewInteractiveFenceRepair(args[3], uid, gid)
		if err == nil {
			return InteractiveFencePreview{State: "repair_required", Plan: &p, FenceInode: p.FenceInode}, nil
		}
		if out, healthy := healthyInteractiveFence(args[3], uid, gid); healthy == nil {
			return out, nil
		}
		return nil, err
	}
	p, err := decodeFenceRepairPlan(args[3])
	if err != nil || p.TargetUID != uid || p.TargetGID != gid {
		return nil, errors.New("service identity or pinned plan changed")
	}
	pending, err := deploy.New(policy.Default(), "/var/lib/mcpshka/deploys").Pending()
	if err != nil || pending {
		return nil, errors.New("deployment authority unavailable or active: HOLD")
	}
	p.Authorization = "admin-session:" + session.ID + ":generation:" + strconv.FormatInt(session.Generation, 10)
	return s.RepairInteractiveFence(p)
}
