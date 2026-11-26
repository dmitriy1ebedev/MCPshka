package projectlease

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RootSealedRetirement is an operator-only plan, not an MCP approval or a proof of owner death.
type RootSealedRetirement struct {
	Path, FenceRoot, GitCommonDir                                            string
	LeaseID                                                                  string
	Generation                                                               uint64
	ExecutionID, Owner, RecordSHA256, SnapshotTree, JournalID, Authorization string
	WorktreeSHA256                                                           string `json:"WorktreeSHA256,omitempty"`
}
type RootRetirementResult struct {
	RetiredLeaseID string
	Recorded       bool
}

func retirementHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b)*2 == n && s == strings.ToLower(s)
}

func rootSealedDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("noncanonical fence")
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return errors.New("fence alias or unavailable")
	}
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm() != 0500 {
		return errors.New("fence must be a root-owned 0500 directory")
	}
	// The boundary itself must not be movable by a service-owned parent.
	parent, e := os.Stat(filepath.Dir(path))
	if e != nil {
		return e
	}
	pst, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || pst.Uid != 0 || parent.Mode().Perm()&0022 != 0 {
		return errors.New("fence parent is mutable by an unprivileged writer")
	}
	return nil
}
func retirementGitCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	fixed := []string{"-c", "safe.directory=*", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "gc.auto=0", "-C", path}
	c := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
	c.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0"}
	return c
}
func retirementGit(ctx context.Context, path string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "status" {
		if err := rejectRecoveryGitExecutables(ctx, path); err != nil {
			return "", err
		}
	}
	c := retirementGitCommand(ctx, path, args...)
	var out recoveryOutput
	c.Stdout = &out
	if e := c.Run(); e != nil {
		if out.Len() >= recoveryGitOutputBudget-64*1024 {
			return "", fmt.Errorf("Git proof overflow: output over the %d MiB recovery budget", recoveryGitOutputBudget>>20)
		}
		return "", errors.New("sealed Git proof failed")
	}
	return strings.TrimSpace(out.String()), nil
}

// RetireRootSealed is deliberately unavailable to the unprivileged MCP daemon.
// It retires the pinned old lease, never authorizes a new writer. OS fencing must
// remain until the operator finishes reconciliation and owns any successor lease.
func (s *Service) RetireRootSealed(p RootSealedRetirement) (RootRetirementResult, error) {
	hold := func(e error) (RootRetirementResult, error) {
		return RootRetirementResult{}, fmt.Errorf("root-sealed retirement HOLD: %w", e)
	}
	if os.Geteuid() != 0 {
		return hold(errors.New("root operator required"))
	}
	if s == nil || s.stateErr != nil || s.database == nil {
		return hold(errors.New("SQLite authority unavailable"))
	}
	if p.LeaseID == "" || p.Owner == "" || !validExecutionID(p.ExecutionID) || !retirementHex(p.RecordSHA256, 64) || !retirementHex(p.SnapshotTree, 40) || !retirementHex(p.JournalID, 64) || strings.TrimSpace(p.Authorization) == "" || len(p.Authorization) > 1024 {
		return hold(errors.New("invalid pinned operator plan"))
	}
	if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path || !filepath.IsAbs(p.GitCommonDir) || filepath.Clean(p.GitCommonDir) != p.GitCommonDir {
		return hold(errors.New("noncanonical resource"))
	}
	journalKey := "project-retirements/v1/" + p.JournalID
	plan, e := json.Marshal(p)
	if e != nil {
		return hold(e)
	}
	// A completed old retirement is replayable even if a different lease now exists.
	prior, e := s.database.Get(journalKey)
	if e == nil {
		if !bytes.Equal(prior, plan) {
			return hold(errors.New("journal pin differs"))
		}
		return RootRetirementResult{RetiredLeaseID: p.LeaseID, Recorded: true}, nil
	}
	if !os.IsNotExist(e) {
		return hold(e)
	}
	if _, e = s.policy.CheckWorkdir(p.Path); e != nil {
		return hold(e)
	}
	if p.Path != p.FenceRoot && !strings.HasPrefix(p.Path, p.FenceRoot+"/") {
		return hold(errors.New("scope is outside immutable boundary"))
	}
	if e = rootSealedDirectory(p.FenceRoot); e != nil {
		return hold(e)
	}
	// A linked worktree needs the common Git directory sealed as well.
	if e = rootSealedDirectory(p.GitCommonDir); e != nil {
		return hold(e)
	}
	scope, e := os.Lstat(p.Path)
	if e != nil || !scope.IsDir() || scope.Mode()&os.ModeSymlink != 0 {
		return hold(errors.New("scope unavailable or aliased"))
	}
	real, e := filepath.EvalSymlinks(p.Path)
	if e != nil || real != p.Path {
		return hold(errors.New("scope alias"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, e := retirementGit(ctx, p.Path, "rev-parse", "--show-toplevel")
	if e != nil || root != p.Path {
		return hold(errors.New("Git root differs"))
	}
	common, e := retirementGit(ctx, p.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || common != p.GitCommonDir {
		return hold(errors.New("Git authority differs"))
	}
	kind, e := retirementGit(ctx, p.Path, "cat-file", "-t", p.SnapshotTree)
	if e != nil || kind != "tree" {
		return hold(errors.New("durable recovery tree unavailable"))
	}
	gatePlan := p
	gatePlan.RecordSHA256 = ""
	gateBody, e := json.Marshal(gatePlan)
	if e != nil {
		return hold(e)
	}
	gateKey := s.writerGateKey(p.Path)
	inserted, e := s.database.PutIfAbsent(gateKey, gateBody)
	if e != nil {
		return hold(e)
	}
	if !inserted {
		existing, e := s.database.Get(gateKey)
		if e != nil {
			return hold(e)
		}
		if !bytes.Equal(existing, gateBody) {
			return hold(errors.New("writer gate differs"))
		}
	}
	key := "leases/" + filepath.Base(s.leasePath(p.Path))
	raw, e := s.database.Get(key)
	if e != nil {
		return hold(e)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != p.RecordSHA256 {
		return hold(errors.New("record changed"))
	}
	var old record
	if e = json.Unmarshal(raw, &old); e != nil {
		return hold(e)
	}
	if old.ProjectRoot != p.Path || leaseIdentity(old) != p.LeaseID || old.Generation != p.Generation || old.ExecutionID != p.ExecutionID || old.Owner != p.Owner || old.OwnerJobID != "" {
		return hold(errors.New("identity changed or managed job owner"))
	}
	if e = noSealedProcessReferences([]string{p.FenceRoot, p.GitCommonDir}); e != nil {
		return hold(e)
	}
	// Only root can undo these fences; recheck their identity immediately before CAS.
	if e = rootSealedDirectory(p.FenceRoot); e != nil {
		return hold(e)
	}
	if e = rootSealedDirectory(p.GitCommonDir); e != nil {
		return hold(e)
	}
	ok, e := s.database.DeleteWithJournal(key, raw, journalKey, plan)
	if e != nil {
		return hold(e)
	}
	if !ok {
		return hold(errors.New("lease or journal changed during CAS"))
	}
	return RootRetirementResult{RetiredLeaseID: p.LeaseID, Recorded: true}, nil
}

func rootSealedWriterAdmission(root string) error {
	return rootSealedWriterAdmissionForUID(root, os.Geteuid())
}
func rootSealedWriterAdmissionForUID(root string, uid int) error {
	info, e := os.Stat(root)
	if e != nil {
		return e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("writer admission filesystem identity unavailable")
	}
	if uid != 0 && st.Uid == 0 && info.Mode().Perm() == 0500 {
		return errors.New("root-sealed operator maintenance: writer admission HOLD")
	}
	return nil
}

func (s *Service) writerGateKey(root string) string {
	return "project-write-gates/v1/" + filepath.Base(s.leasePath(root))
}
func (s *Service) unprivilegedWriterGate(root string) string {
	if os.Geteuid() == 0 {
		return ""
	}
	return s.writerGateKey(root)
}
func (s *Service) writerAdmission(root string) error {
	if e := rootSealedWriterAdmission(root); e != nil {
		return e
	}
	if os.Geteuid() == 0 || s.database == nil {
		return nil
	}
	_, e := s.database.Get(s.writerGateKey(root))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	return errors.New("operator maintenance: writer admission HOLD")
}

// OpenRootWriterGate completes operator maintenance only for its own successor lease.
func (s *Service) OpenRootWriterGate(path, token, gateSHA, journalID string) error {
	if os.Geteuid() != 0 || s == nil || s.database == nil || s.stateErr != nil {
		return errors.New("trusted root SQLite operator required")
	}
	if !retirementHex(gateSHA, 64) || !retirementHex(journalID, 64) {
		return errors.New("invalid gate pin")
	}
	holder, e := s.ValidateHolder(path, token)
	if e != nil {
		return e
	}
	if !strings.HasPrefix(holder.Owner, "operator-root:mcpshka-one-tree-") {
		return errors.New("operator successor lease required")
	}
	key := s.writerGateKey(holder.ProjectRoot)
	raw, e := s.database.Get(key)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != gateSHA {
		return errors.New("gate changed")
	}
	receipt, e := json.Marshal(struct {
		Path, LeaseID, GateSHA string
		Generation             uint64
	}{holder.ProjectRoot, holder.LeaseID, gateSHA, holder.Generation})
	if e != nil {
		return e
	}
	ok, e := s.database.DeleteWithJournal(key, raw, "project-gate-open/v1/"+journalID, receipt)
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("gate or receipt changed")
	}
	return nil
}

// A sealed directory removes path access; an inert control daemon can retain cwd.
// Existing descriptors and mappings are still rejected separately.
func unprivilegedSealedCWD(prefix string) bool {
	b, e := os.ReadFile(prefix + "/status")
	if e != nil || len(b) > 65536 {
		return false
	}
	uidOK, prmOK, effOK, inhOK, ambOK, nnpOK := false, false, false, false, false, false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "Uid:":
			uidOK = len(f) == 5
			for _, v := range f[1:] {
				n, e := strconv.ParseUint(v, 10, 32)
				uidOK = uidOK && e == nil && n != 0
			}
		case "CapPrm:":
			n, e := strconv.ParseUint(f[1], 16, 64)
			prmOK = e == nil && n == 0
		case "CapEff:":
			n, e := strconv.ParseUint(f[1], 16, 64)
			effOK = e == nil && n == 0
		case "CapInh:":
			n, e := strconv.ParseUint(f[1], 16, 64)
			inhOK = e == nil && n == 0
		case "CapAmb:":
			n, e := strconv.ParseUint(f[1], 16, 64)
			ambOK = e == nil && n == 0
		case "NoNewPrivs:":
			nnpOK = f[1] == "1"
		}
	}
	return uidOK && prmOK && effOK && inhOK && ambOK && nnpOK
}

// Old read handles to unlinked regular inodes cannot address current source files.
func detachedReadOnlyFD(entry, dest string) bool {
	if !strings.Contains(entry, "/fd/") || !strings.HasSuffix(dest, " (deleted)") {
		return false
	}
	path := strings.TrimSuffix(dest, " (deleted)")
	if strings.HasPrefix(filepath.Base(path), "#") {
		return false
	}
	info, e := os.Stat(entry)
	if e != nil || !info.Mode().IsRegular() {
		return false
	}
	old, ok := info.Sys().(*syscall.Stat_t)
	if !ok || old.Nlink != 0 {
		return false
	}
	b, e := os.ReadFile(strings.Replace(entry, "/fd/", "/fdinfo/", 1))
	if e != nil || len(b) > 8192 {
		return false
	}
	readonly := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "flags:" {
			flags, e := strconv.ParseUint(f[1], 8, 64)
			readonly = e == nil && flags&uint64(syscall.O_ACCMODE) == uint64(syscall.O_RDONLY)
		}
	}
	if !readonly {
		return false
	}
	current, e := os.Stat(path)
	if e == nil {
		st, ok := current.Sys().(*syscall.Stat_t)
		if !ok || (st.Dev == old.Dev && st.Ino == old.Ino) {
			return false
		}
	} else if !os.IsNotExist(e) {
		return false
	}
	again, e := os.Stat(entry)
	if e != nil {
		return false
	}
	st, ok := again.Sys().(*syscall.Stat_t)
	return ok && st.Dev == old.Dev && st.Ino == old.Ino && st.Nlink == 0
}
