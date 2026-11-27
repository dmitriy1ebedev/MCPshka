package projectlease

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

type Service struct {
	dir           string
	policy        *policy.Policy
	git           *gitops.Service
	database      *statestore.DB
	stateErr      error
	interactiveMu sync.Mutex // Serializes an interactive effect against lease release in this daemon.
}

// A lease never expires automatically. A heartbeat is evidence of owner
// activity, not permission for another writer to take over.
const leaseReviewAfter = 30 * time.Minute

type record struct {
	LeaseID         string    `json:"lease_id,omitempty"`
	Generation      uint64    `json:"generation,omitempty"`
	ProjectRoot     string    `json:"project_root"`
	Owner           string    `json:"owner"`
	Token           string    `json:"token"`
	AcquiredAt      time.Time `json:"acquired_at"`
	ExecutionID     string    `json:"execution_id,omitempty"`
	HeartbeatAt     time.Time `json:"heartbeat_at,omitempty"`
	OwnerJobID      string    `json:"owner_job_id,omitempty"`
	OwnerPID        int       `json:"owner_pid,omitempty"`
	OwnerStartTicks uint64    `json:"owner_start_ticks,omitempty"`
	OwnerBootID     string    `json:"owner_boot_id,omitempty"`
	CommonGitDir    string    `json:"common_git_dir,omitempty"`
}

type Status struct {
	LeaseID                string     `json:"lease_id,omitempty"`
	Generation             uint64     `json:"generation"`
	OwnerJobID             string     `json:"owner_job_id,omitempty"`
	OwnerPID               int        `json:"owner_pid,omitempty"`
	OwnerStartTicks        uint64     `json:"owner_start_ticks,omitempty"`
	OwnerBootID            string     `json:"owner_boot_id,omitempty"`
	CommonGitDir           string     `json:"common_git_dir,omitempty"`
	Held                   bool       `json:"held"`
	ProjectRoot            string     `json:"project_root"`
	Owner                  string     `json:"owner,omitempty"`
	AcquiredAt             time.Time  `json:"acquired_at,omitempty"`
	ExecutionID            string     `json:"execution_id,omitempty"`
	HeartbeatAt            time.Time  `json:"heartbeat_at,omitempty"`
	LastHeartbeatAt        *time.Time `json:"last_heartbeat_at,omitempty"`
	AgeSeconds             int64      `json:"age_seconds,omitempty"`
	ReconciliationRequired bool       `json:"reconciliation_required"`
	OwnerLiveness          string     `json:"owner_liveness,omitempty"`
	Reclaimable            bool       `json:"reclaimable"`
	// RecoveryHold is why maintenance keeps a managed lease (MCP-265).
	RecoveryHold string `json:"recovery_hold,omitempty"`
}

type AcquireResult struct {
	Status Status `json:"status"`
	Token  string `json:"token"`
}

type ReleaseResult struct {
	ProjectRoot string `json:"project_root"`
	Released    bool   `json:"released"`
}

// AdministrativeReview reports the evidence required before an operator may
// authorize transferring a writer lock. It never changes ownership.
type AdministrativeReview struct {
	LeaseID               string `json:"lease_id,omitempty"`
	Generation            uint64 `json:"generation"`
	State                 string `json:"state"`
	FencingState          string `json:"fencing_state"`
	ProjectRoot           string `json:"project_root"`
	Owner                 string `json:"owner"`
	ExecutionID           string `json:"execution_id"`
	OwnerBound            bool   `json:"owner_bound"`
	RequiresAdministrator bool   `json:"requires_administrator"`
	Reason                string `json:"reason"`
}

func (s *Service) ReviewAdministrativeTransfer(path string) (AdministrativeReview, error) {
	root, err := s.projectRoot(path)
	if err != nil {
		return AdministrativeReview{}, err
	}
	r, err := s.readRecord(s.leasePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return AdministrativeReview{ProjectRoot: root, State: "RELEASED", FencingState: "not_required"}, nil
	}
	if err != nil {
		return AdministrativeReview{}, err
	}
	bound := r.OwnerJobID != "" && r.OwnerPID > 0 && r.OwnerStartTicks > 0 && r.OwnerBootID != ""
	reason := "verify bound process, job and deployment before administrative transfer"
	if !bound {
		reason = "legacy/unbound owner: require explicit administrator reconciliation and deployment probe"
	}
	state := "UNKNOWN"
	if publicStatus(r).ReconciliationRequired {
		state = "SUSPECT"
	}
	return AdministrativeReview{LeaseID: leaseIdentity(r), Generation: r.Generation, State: state, FencingState: "not_verified", ProjectRoot: root, Owner: r.Owner, ExecutionID: r.ExecutionID, OwnerBound: bound, RequiresAdministrator: true, Reason: reason}, nil
}

// TransferProof is supplied by an authorized administrative controller only
// after independently reconciling jobs, processes, deployment and audit policy.
// A caller cannot infer these facts from lock age or an absent heartbeat.
type TransferProof struct {
	// Only a trusted in-package controller may supply this critical section.
	// It must verify the pinned writer, hold admission/process fencing through
	// commit, and invoke commit synchronously exactly once. Nil always HOLDs.
	writerFence         func(func() error) error
	ExpectedLeaseID     string
	ExpectedGeneration  uint64
	ExpectedExecutionID string
	ExpectedOwner       string
	Authorized          bool
	JobsQuiescent       bool
	ProcessesQuiescent  bool
	DeploymentQuiescent bool
	AuditRecorded       bool
}

// TransferAdministrative rotates holder credentials, not process write rights.
// It must HOLD for unbound interactive owners without an enforceable fence.
// This internal primitive installs a new
// token in one SQLite CAS. A stale old token cannot renew or release it.
// This primitive deliberately does not expose an MCP endpoint: the trusted
// administrative controller must obtain and verify the proof independently.
func (s *Service) TransferAdministrative(path, newOwner string, proof TransferProof) (AcquireResult, error) {
	if s.stateErr != nil {
		return AcquireResult{}, s.stateErr
	}
	if s.database == nil {
		return AcquireResult{}, errors.New("administrative transfer requires SQLite CAS authority")
	}
	if !proof.Authorized || !proof.JobsQuiescent || !proof.ProcessesQuiescent || !proof.DeploymentQuiescent || !proof.AuditRecorded {
		return AcquireResult{}, errors.New("administrative transfer HOLD: incomplete authorization or reconciliation")
	}
	if proof.writerFence == nil {
		return AcquireResult{}, errors.New("administrative transfer HOLD: enforceable writer fence unavailable")
	}
	newOwner = strings.TrimSpace(newOwner)
	if newOwner == "" || proof.ExpectedOwner == "" || !validExecutionID(proof.ExpectedExecutionID) {
		return AcquireResult{}, errors.New("administrative transfer requires pinned owner, execution and new owner")
	}
	root, err := s.projectRoot(path)
	if err != nil {
		return AcquireResult{}, err
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	previous, err := s.database.Get(key)
	if err != nil {
		return AcquireResult{}, err
	}
	var old record
	if err := json.Unmarshal(previous, &old); err != nil {
		return AcquireResult{}, errors.New("invalid existing writer lock")
	}
	if old.ProjectRoot != root || old.Owner != proof.ExpectedOwner || old.ExecutionID != proof.ExpectedExecutionID || leaseIdentity(old) != proof.ExpectedLeaseID || old.Generation != proof.ExpectedGeneration {
		return AcquireResult{}, errors.New("administrative transfer HOLD: writer identity changed")
	}
	if old.OwnerJobID == "" || old.OwnerPID <= 0 || old.OwnerStartTicks == 0 || old.OwnerBootID == "" {
		return AcquireResult{}, errors.New("administrative transfer HOLD: unbound writer cannot be fenced")
	}
	if old.Generation == ^uint64(0) {
		return AcquireResult{}, errors.New("administrative transfer HOLD: generation exhausted")
	}
	token, err := randomToken()
	if err != nil {
		return AcquireResult{}, err
	}
	executionID, err := randomToken()
	if err != nil {
		return AcquireResult{}, err
	}
	// Execution identity is 32 hex chars; the holder secret remains 48.
	now := time.Now().UTC()
	next := record{LeaseID: leaseIdentity(old), Generation: old.Generation + 1, ProjectRoot: root, Owner: newOwner, Token: token, ExecutionID: executionID[:32], AcquiredAt: now, HeartbeatAt: now}
	replacement, err := json.Marshal(next)
	if err != nil {
		return AcquireResult{}, err
	}
	replacement = append(replacement, '\n')
	committed := false
	// The controller must not retain the commit closure and invoke it after
	// the writer fence has been released. Guard both state and CAS under one
	// mutex so late callbacks cannot race the return path.
	var fenceMu sync.Mutex
	fenceActive := true
	err = proof.writerFence(func() error {
		fenceMu.Lock()
		defer fenceMu.Unlock()
		if !fenceActive {
			return errors.New("administrative transfer HOLD: fence callback expired")
		}
		if committed {
			return errors.New("administrative transfer HOLD: fence callback repeated")
		}
		ok, err := s.database.ReplaceUnlessGate(key, previous, replacement, s.unprivilegedWriterGate(root))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("administrative transfer HOLD: writer changed during transfer")
		}
		committed = true
		return nil
	})
	fenceMu.Lock()
	fenceActive = false
	fenceMu.Unlock()
	if err != nil {
		// A post-CAS error is an UNKNOWN outcome, never permission to replay.
		if committed {
			return AcquireResult{}, fmt.Errorf("administrative transfer outcome UNKNOWN: ownership committed; reconcile before retry: %w", err)
		}
		return AcquireResult{}, err
	}
	if !committed {
		return AcquireResult{}, errors.New("administrative transfer HOLD: fence did not commit")
	}
	return AcquireResult{Status: publicStatus(next), Token: token}, nil
}

type HeartbeatResult struct {
	Status Status `json:"status"`
}

func New(dir string, p *policy.Policy) *Service {
	if p == nil {
		p = policy.Default()
	}
	db, err := statestore.Active(filepath.Dir(dir))
	return &Service{dir: dir, policy: p, git: gitops.New(p), database: db, stateErr: err}
}

func (s *Service) Acquire(path, owner string) (AcquireResult, error) {
	return s.AcquireTracked(path, owner, "")
}

// AcquireTracked records a stable operation identity alongside a single-writer
// lease. Legacy Acquire remains compatible, but untracked leases must never be
// automatically reclaimed on elapsed time alone.
func (s *Service) AcquireTracked(path, owner, executionID string) (AcquireResult, error) {
	return s.acquire(path, owner, executionID, "", nil)
}

// AcquireCommit is the daemon's trusted same-database receipt transaction.
type AcquireCommit func(*statestore.DB, string, []byte, string, AcquireResult) error

func (s *Service) AcquireJournaled(path, owner, executionID string, commit AcquireCommit) (AcquireResult, error) {
	if s.database == nil || executionID == "" || commit == nil {
		return AcquireResult{}, errors.New("journaled lease requires SQLite and its original operation receipt")
	}
	return s.acquire(path, owner, executionID, "", commit)
}

func (s *Service) acquire(path, owner, executionID, ownerJobID string, commit AcquireCommit) (AcquireResult, error) {
	if executionID != "" && !validExecutionID(executionID) {
		return AcquireResult{}, errors.New("invalid lease execution id")
	}
	if s.stateErr != nil {
		return AcquireResult{}, s.stateErr
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return AcquireResult{}, errors.New("lease owner is required")
	}
	root, err := s.projectRoot(path)
	if err != nil {
		return AcquireResult{}, err
	}
	if err := s.writerAdmission(root); err != nil {
		return AcquireResult{}, err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return AcquireResult{}, fmt.Errorf("create lease directory: %w", err)
	}
	// Never issue an interactive holder that cannot use or release its fence.
	if s.database != nil && ownerJobID == "" {
		fence, err := s.lockInteractiveFence(root)
		if err != nil {
			return AcquireResult{}, err
		}
		defer fence.Close()
	}
	token, err := randomToken()
	if err != nil {
		return AcquireResult{}, err
	}
	now := time.Now().UTC()
	r := record{
		ProjectRoot: root,
		Owner:       owner,
		Token:       token,
		Generation:  1,
		AcquiredAt:  now,
		ExecutionID: executionID,
		OwnerJobID:  ownerJobID,
		HeartbeatAt: now,
	}
	// Sibling worktrees share one object store and refs: recording it shows
	// which leases touch the same repository (P4: lease identity).
	r.CommonGitDir, _ = gitops.CommonDir(root)
	data, err := json.Marshal(r)
	if err != nil {
		return AcquireResult{}, err
	}
	data = append(data, '\n')
	if s.database != nil {
		if commit != nil {
			out := AcquireResult{Status: publicStatus(r), Token: token}
			if err := commit(s.database, "leases/"+filepath.Base(s.leasePath(root)), data, s.unprivilegedWriterGate(root), out); err != nil {
				return AcquireResult{}, err
			}
			return out, nil
		}
		ok, err := s.database.PutIfAbsentUnlessGate("leases/"+filepath.Base(s.leasePath(root)), data, s.unprivilegedWriterGate(root))
		if err != nil {
			return AcquireResult{}, err
		}
		if !ok {
			return AcquireResult{}, errors.New("project lease already held")
		}
		return AcquireResult{Status: publicStatus(r), Token: token}, nil
	}

	tmp, err := os.CreateTemp(s.dir, ".lease-")
	if err != nil {
		return AcquireResult{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return AcquireResult{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return AcquireResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return AcquireResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return AcquireResult{}, err
	}

	final := s.leasePath(root)
	if err := os.Link(tmpName, final); err != nil {
		if os.IsExist(err) {
			held, readErr := s.readRecord(final)
			if readErr != nil {
				return AcquireResult{}, fmt.Errorf("project lease already exists and cannot be read: %w", readErr)
			}
			return AcquireResult{}, fmt.Errorf("project lease held by %q since %s", held.Owner, held.AcquiredAt.Format(time.RFC3339))
		}
		return AcquireResult{}, fmt.Errorf("acquire project lease: %w", err)
	}
	_ = syncDir(s.dir)
	return AcquireResult{
		Status: publicStatus(r),
		Token:  token,
	}, nil
}

// Heartbeat retains the older API while using the tracked, token-bound
// SQLite CAS heartbeat implementation. Legacy leases remain fail-closed.
func (s *Service) Heartbeat(path, token string) (HeartbeatResult, error) {
	status, err := s.HeartbeatRefresh(path, token)
	if err != nil {
		return HeartbeatResult{}, err
	}
	return HeartbeatResult{Status: status}, nil
}

func (s *Service) Status(path string) (Status, error) {
	root, err := s.projectRoot(path)
	if err != nil {
		// A lease whose worktree was removed is still readable by its exact
		// recorded root, so stale_release can probe and drop it (MCP-167).
		if errors.Is(err, os.ErrNotExist) && s.staleReleasePath(path) == nil {
			r, e := s.readRecord(s.leasePath(path))
			if e == nil && r.ProjectRoot == path {
				return publicStatus(r), nil
			}
			// Released already: the after-probe of stale_release sees it free.
			if errors.Is(e, os.ErrNotExist) {
				return Status{Held: false, ProjectRoot: path}, nil
			}
		}
		return Status{}, err
	}
	r, err := s.readRecord(s.leasePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return Status{Held: false, ProjectRoot: root}, nil
	}
	if err != nil {
		return Status{}, err
	}
	out := publicStatus(r)
	if r.OwnerJobID != "" {
		out.RecoveryHold = s.RecoveryHold(root)
	}
	return out, nil
}

// lockInteractiveFence protects a synchronous holder effect and lease release
// across distinct Service instances and OS processes. It is advisory by
// design: native project jobs remain fenced separately by Guard and SQLite.
func (s *Service) lockInteractiveFence(root string) (*os.File, error) {
	if s.database == nil || s.stateErr != nil {
		return nil, errors.New("interactive fencing requires SQLite authority: HOLD")
	}
	dir, err := os.Lstat(s.dir)
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0022 != 0 {
		return nil, errors.New("private lease directory unavailable: HOLD")
	}
	name := s.leasePath(root) + ".effect.lock"
	owner, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || (os.Geteuid() != 0 && owner.Uid != uint32(os.Geteuid())) {
		return nil, errors.New("private lease directory owner unavailable: HOLD")
	}
	fd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	created := err == nil
	if errors.Is(err, syscall.EEXIST) {
		fd, err = syscall.Open(name, syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, errors.New("interactive fence unavailable: HOLD")
	}
	f := os.NewFile(uintptr(fd), name)
	// Root constructors must not leave new locks inaccessible to the service.
	if created && os.Geteuid() == 0 && owner.Uid != 0 {
		if err := f.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			f.Close()
			return nil, errors.New("new interactive fence ownership failed: HOLD")
		}
	}
	fail := func() (*os.File, error) {
		_ = f.Close()
		return nil, errors.New("interactive fence untrusted or busy: HOLD")
	}
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || actual.Mode().Perm()&0077 != 0 {
		return fail()
	}
	identity, ok := actual.Sys().(*syscall.Stat_t)
	if !ok || identity.Nlink != 1 || identity.Uid != owner.Uid {
		return fail()
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail()
	}
	linked, err := os.Lstat(name)
	if err != nil || !os.SameFile(actual, linked) {
		return fail()
	}
	return f, nil // close releases flock on error, completion, or process death
}

func (s *Service) Release(path, token string) (ReleaseResult, error) {
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	if token == "" {
		return ReleaseResult{}, errors.New("lease token is required")
	}
	root, err := s.releaseRoot(path)
	if err != nil {
		return ReleaseResult{}, err
	}
	// Legacy file-only leases cannot authorize interactive effects. Preserve
	// their token-checked release without pretending SQLite fencing exists.
	if s.database != nil {
		fence, err := s.lockInteractiveFence(root)
		if err != nil {
			return ReleaseResult{}, err
		}
		defer fence.Close()
	}
	final := s.leasePath(root)
	r, err := s.readRecord(final)
	if err != nil {
		return ReleaseResult{}, err
	}
	if len(token) != len(r.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) != 1 {
		return ReleaseResult{}, errors.New("lease token does not match current holder")
	}
	if s.database != nil {
		data, err := s.database.Get("leases/" + filepath.Base(final))
		if err != nil {
			return ReleaseResult{}, err
		}
		var current record
		if err = json.Unmarshal(data, &current); err != nil {
			return ReleaseResult{}, err
		}
		if len(token) != len(current.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(current.Token)) != 1 {
			return ReleaseResult{}, errors.New("lease token changed")
		}
		ok, err := s.database.Delete("leases/"+filepath.Base(final), data)
		if err != nil || !ok {
			return ReleaseResult{}, errors.New("lease changed during release")
		}
		return ReleaseResult{ProjectRoot: root, Released: true}, nil
	}
	if err := os.Remove(final); err != nil {
		return ReleaseResult{}, fmt.Errorf("release project lease: %w", err)
	}
	_ = syncDir(s.dir)
	return ReleaseResult{ProjectRoot: root, Released: true}, nil
}

// A removed checkout still has a lease and an effect lock in the state directory.
func (s *Service) releaseRoot(path string) (string, error) {
	root, err := s.projectRoot(path)
	if !errors.Is(err, os.ErrNotExist) || s.database == nil || s.staleReleasePath(path) != nil {
		return root, err
	}
	r, readErr := s.readRecord(s.leasePath(path))
	if readErr != nil || r.ProjectRoot != path {
		return "", err
	}
	return path, nil
}

func (s *Service) projectRoot(path string) (string, error) {
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	base := real
	if !info.IsDir() {
		base = filepath.Dir(real)
	}
	if root, err := s.git.RepoRoot(base); err == nil {
		return filepath.Clean(root), nil
	}
	return filepath.Clean(base), nil
}

func (s *Service) leasePath(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

func (s *Service) readRecord(path string) (record, error) {
	if s.stateErr != nil {
		return record{}, s.stateErr
	}
	var data []byte
	var err error
	if s.database != nil {
		data, err = s.database.Get("leases/" + filepath.Base(path))
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return record{}, err
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return record{}, fmt.Errorf("decode lease: %w", err)
	}
	if r.ProjectRoot == "" || r.Owner == "" || r.Token == "" || r.AcquiredAt.IsZero() {
		return record{}, errors.New("invalid lease record")
	}
	return r, nil
}

func publicStatus(r record) Status {
	return publicStatusAt(r, time.Now().UTC())
}

func publicStatusAt(r record, now time.Time) Status {
	last := r.AcquiredAt
	var heartbeat *time.Time
	if !r.HeartbeatAt.IsZero() {
		t := r.HeartbeatAt
		heartbeat = &t
		if t.After(last) {
			last = t
		}
	}
	age := now.Sub(last)
	if age < 0 {
		age = 0
	}
	// This is an advisory signal only: age does not prove owner death.
	review := now.Sub(last) >= leaseReviewAfter || last.After(now.Add(5*time.Minute))
	return Status{
		Held:    true,
		LeaseID: leaseIdentity(r), Generation: r.Generation,
		OwnerJobID: r.OwnerJobID, OwnerPID: r.OwnerPID, OwnerStartTicks: r.OwnerStartTicks, OwnerBootID: r.OwnerBootID,
		CommonGitDir:           r.CommonGitDir,
		ProjectRoot:            r.ProjectRoot,
		Owner:                  r.Owner,
		AcquiredAt:             r.AcquiredAt,
		ExecutionID:            r.ExecutionID,
		HeartbeatAt:            r.HeartbeatAt,
		LastHeartbeatAt:        heartbeat,
		AgeSeconds:             int64(age / time.Second),
		ReconciliationRequired: review,
		OwnerLiveness:          "unverified",
		Reclaimable:            false,
	}
}

// Legacy records get a stable non-secret identity without rewriting authority.
// New acquisitions have fresh holder entropy; handoffs retain this ID and advance generation.
func leaseIdentity(r record) string {
	if r.LeaseID != "" {
		return r.LeaseID
	}
	sum := sha256.Sum256([]byte(r.Token))
	return hex.EncodeToString(sum[:16])
}

func randomToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate lease token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// ValidateHolder prevents a cached acquisition receipt from resurrecting a
// released/replaced lease. Owner names alone are not fencing identities.
func (s *Service) ValidateHolder(path, token string) (Status, error) {
	root, err := s.projectRoot(path)
	if err != nil {
		return Status{}, err
	}
	// Plain errors: acquire calls this after its effect, so only the caller
	// that checks first may mark a refusal no-effect (jobs admission does).
	r, err := s.readRecord(s.leasePath(root))
	if err != nil {
		return Status{}, errors.New("lease receipt is stale: no lease is held on " + root + "; omit lease_token if your lease is on another worktree, or reconcile and use a new acquisition key")
	}
	if token == "" || len(token) != len(r.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) != 1 {
		return Status{}, errors.New("lease receipt belongs to a previous holder: reconcile")
	}
	return publicStatus(r), nil
}

// BusyCause names the current holder when an acquire failed on a held lease
// (MCP-186: the busy lease surfaced as an idempotency conflict); else cause.
func (s *Service) BusyCause(path, owner string, cause error) error {
	st, err := s.Status(path)
	if err != nil || !st.Held || st.Owner == owner {
		return cause
	}
	return fmt.Errorf("LEASE_BUSY: %s is held by %s since %s; wait for it or prove it stale (project_lease_admin_stale_preview): %w", st.ProjectRoot, st.Owner, st.AcquiredAt.Format(time.RFC3339), cause)
}
