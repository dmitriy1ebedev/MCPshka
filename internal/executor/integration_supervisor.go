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
	"time"

	"mcpshka/internal/integrationqueue"
)

// Stored before launching the integration worker. It binds its permanently
// closable admission to the exact repository reservation. Test/canary workers
// are not integration writers and cannot supply this binding through an API.
type integrationWorkerBinding struct {
	Version  int              `json:"version"`
	Claim    IntegrationClaim `json:"claim"`
	Pin      Pin              `json:"pin"`
	LaunchID string           `json:"launch_id"`
}

func integrationWorkerKey(execution string) string {
	return "executor/integration-workers/" + execution + ".json"
}

func (c *Controller) bindIntegrationWorker(ctx context.Context, claim IntegrationClaim, pin Pin, launch string) error {
	current, _, err := c.readIntegrationClaim(ctx, claim.Repository.CommonDir)
	if err != nil || !sameIntegrationClaim(current, claim) || current.State != "HELD" || !validPin(pin) || pin.ExecutionID != claim.ExecutionID || pin.Generation != claim.Generation || !idPattern.MatchString(launch) {
		return ErrHold
	}
	a, err := c.integrationApprovalForID(ctx, claim.ApprovalID)
	if err != nil || pin.PermitID == a.TestPin.PermitID || launch == a.LaunchID {
		return ErrHold
	}
	s, err := c.Inspect(ctx, pin)
	if err != nil || s.State != "ACTIVE" || len(s.Launches) != 1 || s.Launches[0].ID != launch || s.Launches[0].State != "LAUNCH_INTENT" || s.Launches[0].Identity != nil {
		return ErrHold
	}
	claim.State = "HELD"
	raw, _ := json.Marshal(integrationWorkerBinding{Version: 1, Claim: claim, Pin: pin, LaunchID: launch})
	ok, err := c.db.PutIfAbsent(integrationWorkerKey(claim.ExecutionID), raw)
	if err != nil {
		return err
	}
	if !ok {
		prior, err := c.db.GetContext(ctx, integrationWorkerKey(claim.ExecutionID))
		if err != nil || !bytes.Equal(prior, raw) {
			return ErrHold
		}
	}
	return nil
}

func (c *Controller) integrationApprovalForID(ctx context.Context, id string) (IntegrationApproval, error) {
	if c.db == nil || !digestPattern.MatchString(id) {
		return IntegrationApproval{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, "executor/integration-approvals/"+id+".json")
	var a IntegrationApproval
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &a) != nil || a.ID != id {
		return IntegrationApproval{}, ErrHold
	}
	return c.inspectIntegrationApproval(ctx, integrationqueue.RuntimeItem{Request: a.Request, ExecutionID: a.ExecutionID, State: integrationqueue.Pending}, a.Repository)
}

func claimMatchesApproval(claim IntegrationClaim, a IntegrationApproval) bool {
	return claim.ApprovalID == a.ID && claim.Repository == a.Repository && claim.ExecutionID == a.ExecutionID && claim.Owner == a.Request.Owner && claim.Candidate == a.Request.Commit
}

func (c *Controller) integrationClaimForApproval(ctx context.Context, a IntegrationApproval) (IntegrationClaim, error) {
	current, _, err := c.readIntegrationClaim(ctx, a.Repository.CommonDir)
	if err != nil {
		return IntegrationClaim{}, ErrHold
	}
	if claimMatchesApproval(current, a) {
		return current, nil
	}
	raw, err := c.db.GetContext(ctx, integrationCompletionKey(a.ExecutionID))
	completed, decodeErr := decodeIntegrationClaim(raw, a.Repository.CommonDir)
	if err != nil || decodeErr != nil || (completed.State != "RELEASED" && completed.State != "FAILED") || !claimMatchesApproval(completed, a) || current.Repository != completed.Repository || current.Generation <= completed.Generation {
		return IntegrationClaim{}, ErrHold
	}
	return completed, nil
}

type integrationRecoveryBackend interface {
	WithWriterFence(context.Context, IntegrationApproval, func() error) error
	VerifyStopped(context.Context, string, Identity) error
	Observe(context.Context, IntegrationApproval) (integrationObservation, error)
}

// No external mutation is retried. Even durable FENCED state is insufficient:
// recovery freshly verifies the exact incarnation before probing the Git effect.
func (c *Controller) recoverIntegrationClaim(ctx context.Context, a IntegrationApproval, backend integrationRecoveryBackend) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	stored, err := c.integrationApprovalForID(ctx, a.ID)
	if err != nil || stored != a || backend == nil {
		return ErrHold
	}
	claim, err := c.integrationClaimForApproval(ctx, a)
	if err != nil {
		return ErrHold
	}
	if claim.State == "RELEASED" {
		return nil
	}
	if claim.State != "EFFECT_INTENT" {
		return ErrHold
	}
	raw, err := c.db.GetContext(ctx, integrationWorkerKey(claim.ExecutionID))
	if err != nil || len(raw) > 8192 {
		return ErrHold
	}
	var binding integrationWorkerBinding
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&binding) != nil || d.Decode(new(any)) != io.EOF || binding.Version != 1 || binding.Claim.State != "HELD" || !sameIntegrationClaim(binding.Claim, claim) || !validPin(binding.Pin) || binding.Pin.ExecutionID != claim.ExecutionID || binding.Pin.Generation != claim.Generation || binding.Pin.PermitID == a.TestPin.PermitID || !idPattern.MatchString(binding.LaunchID) || binding.LaunchID == a.LaunchID {
		return ErrHold
	}
	s, err := c.Inspect(ctx, binding.Pin)
	if err != nil || s.State != "FENCED" || len(s.Launches) != 1 || s.Launches[0].ID != binding.LaunchID || s.Launches[0].State != "FENCED" || s.Launches[0].Identity == nil {
		return ErrHold
	}
	return backend.WithWriterFence(ctx, a, func() error {
		if err := backend.VerifyStopped(ctx, binding.LaunchID, *s.Launches[0].Identity); err != nil {
			return ErrHold
		}
		observed, err := backend.Observe(ctx, a)
		if err != nil {
			return ErrHold
		}
		return c.finishIntegrationClaim(ctx, claim, observed)
	})
}

type liveIntegrationRecovery struct {
	SystemdFence
	controller *Controller
}

func (b liveIntegrationRecovery) WithWriterFence(ctx context.Context, a IntegrationApproval, fn func() error) error {
	// Called under the root integration lock. Only newly born root-private stores
	// can exclude inherited native UID descriptors. Legacy Git remains HOLD.
	if b.controller == nil || !b.controller.authority || os.Geteuid() != 0 || fn == nil {
		return ErrHold
	}
	if _, err := b.controller.protectedRepositoryBirth(ctx, ProtectedRepositoryRoot, a.Repository, 0); err != nil {
		return ErrHold
	}
	if err := verifyProtectedRepositoryStorage(ctx, a.Repository); err != nil {
		return ErrHold
	}
	return fn()
}

func (b liveIntegrationRecovery) Observe(ctx context.Context, a IntegrationApproval) (integrationObservation, error) {
	if filepath.Dir(a.Repository.CommonDir) == ProtectedRepositoryRoot {
		if b.controller == nil || !b.controller.authority || os.Geteuid() != 0 {
			return integrationObservation{}, ErrHold
		}
		return b.controller.observeProtectedIntegration(ctx, ProtectedRepositoryRoot, a, 0)
	}
	repo, err := integrationRepository(ctx, a.Request.Path)
	if err != nil || repo != a.Repository {
		return integrationObservation{}, ErrHold
	}
	ref := "refs/heads/" + a.Request.TargetBranch
	sum := sha256.Sum256([]byte(a.ExecutionID))
	receiptRef := "refs/mcpshka/integration/" + hex.EncodeToString(sum[:])
	readDirect := func(ref string) (string, error) {
		raw, err := gitSnapshotRead(ctx, a.Request.Path, "for-each-ref", "--format=%(refname)%09%(objectname)%09%(symref)", ref)
		if err != nil || string(raw) != ref+"\t"+a.Request.Commit+"\t\n" {
			return "", ErrHold
		}
		return a.Request.Commit, nil
	}
	head, err := readDirect(ref)
	if err != nil {
		return integrationObservation{}, ErrHold
	}
	receipt, err := readDirect(receiptRef)
	if err != nil {
		return integrationObservation{}, ErrHold
	}
	end, err := integrationRepository(ctx, a.Request.Path)
	if err != nil || end != repo {
		return integrationObservation{}, ErrHold
	}
	return integrationObservation{Repository: repo, Head: head, Receipt: receipt}, nil
}

// RecoverIntegration is a fixed-ID root supervisor boundary. The daemon cannot
// provide a worker identity, manifest, success flag or observed target SHA.
// It does not launch/kill workers, acquire legacy leases or repeat Git updates.
func RecoverIntegration(ctx context.Context, id string) (IntegrationClaim, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationClaim{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationClaim{}, err
	}
	err = c.withIntegrationLock(ctx, a.Repository, func() error { return c.recoverIntegrationClaim(ctx, a, liveIntegrationRecovery{controller: c}) })
	if err != nil {
		return IntegrationClaim{}, err
	}
	return c.integrationClaimForApproval(ctx, a)
}

func IntegrationRecoveryStatus(ctx context.Context, id string) (IntegrationClaim, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	a, err := IntegrationApprovalStatus(ctx, id)
	if err != nil {
		return IntegrationClaim{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return IntegrationClaim{}, err
	}
	return c.integrationClaimForApproval(ctx, a)
}

// The lock lives in sealed root storage, never the worker-writable Git directory.
// Future launch/admission code must take this same lock and enforce the native
// writer barrier; this recovery path does not claim to fence legacy writers.
func (c *Controller) withIntegrationLock(ctx context.Context, repo RepositoryIdentity, fn func() error) error {
	if !c.authority || os.Geteuid() != 0 || fn == nil || repo.Device == 0 || repo.Inode == 0 || !filepath.IsAbs(repo.CommonDir) || filepath.Clean(repo.CommonDir) != repo.CommonDir || !strings.HasPrefix(repo.CommonDir, "/root/repos/") {
		return ErrHold
	}
	if _, err := OpenAuthority(); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(repo.CommonDir))
	path := filepath.Join(AuthorityRoot, "integration-"+hex.EncodeToString(sum[:])+".lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return ErrHold
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 {
		return ErrHold
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
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
	defer syscall.Flock(fd, syscall.LOCK_UN)
	return fn()
}
