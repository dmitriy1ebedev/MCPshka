package projectlease

// Root-sealed lease recovery: journal replay and its preview.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RootSealedRecovery transfers a legacy, unbound lease to a root-owned
// maintenance holder. An authenticated operator must first seal the source
// and common Git authority (root-owned 0500) and pin a durable Git tree.
// The fence and database writer gate remain CLOSED after handoff.
type RootSealedRecovery struct {
	RootSealedRetirement
	NewOwner string
}

type RootRecoveryJournal struct {
	Schema         string `json:"schema"`
	At             string `json:"at"`
	Path           string `json:"path"`
	OldLeaseID     string `json:"old_lease_id"`
	OldGeneration  uint64 `json:"old_generation"`
	NewGeneration  uint64 `json:"new_generation"`
	OldOwner       string `json:"old_owner"`
	NewOwner       string `json:"new_owner"`
	OldExecutionID string `json:"old_execution_id"`
	NewExecutionID string `json:"new_execution_id"`
	PreviousSHA256 string `json:"previous_sha256"`
	SnapshotTree   string `json:"snapshot_tree"`
	Authorization  string `json:"authorization"`
	SnapshotRef    string `json:"snapshot_ref,omitempty"`
	WorktreeSHA256 string `json:"worktree_sha256,omitempty"`
}

// RecoverRootSealed executes one root-only, process-checked, journaled SQLite
// CAS. A lost response is UNKNOWN: inspect the new holder and journal rather
// than retrying. No token is persisted in the immutable audit journal.
func (s *Service) RecoverRootSealed(p RootSealedRecovery) (AcquireResult, error) {
	hold := func(err error) (AcquireResult, error) {
		return AcquireResult{}, fmt.Errorf("root-sealed legacy recovery HOLD: %w", err)
	}
	if os.Geteuid() != 0 || s == nil || s.stateErr != nil || s.database == nil {
		return hold(errors.New("trusted root SQLite operator required"))
	}
	if p.LeaseID == "" || p.Owner == "" || (p.ExecutionID != "" && !validExecutionID(p.ExecutionID)) ||
		!retirementHex(p.RecordSHA256, 64) || !retirementHex(p.SnapshotTree, 40) ||
		!retirementHex(p.JournalID, 64) || strings.TrimSpace(p.Authorization) == "" ||
		len(p.Authorization) > 1024 ||
		!strings.HasPrefix(p.NewOwner, "operator-root:mcpshka-one-tree-") ||
		len(p.NewOwner) > 128 || p.NewOwner == p.Owner {
		return hold(errors.New("invalid operator recovery plan"))
	}
	if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path ||
		!filepath.IsAbs(p.FenceRoot) || filepath.Clean(p.FenceRoot) != p.FenceRoot ||
		!filepath.IsAbs(p.GitCommonDir) || filepath.Clean(p.GitCommonDir) != p.GitCommonDir ||
		(p.Path != p.FenceRoot && !strings.HasPrefix(p.Path, p.FenceRoot+"/")) {
		return hold(errors.New("noncanonical or uncontained recovery scope"))
	}
	if _, err := s.policy.CheckWorkdir(p.Path); err != nil {
		return hold(err)
	}
	if err := s.recoveryQuiescent(p.Path); err != nil {
		return hold(err)
	}
	if err := rootSealedDirectory(p.FenceRoot); err != nil {
		return hold(err)
	}
	if err := rootSealedDirectory(p.GitCommonDir); err != nil {
		return hold(err)
	}
	info, err := os.Lstat(p.Path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return hold(errors.New("Git scope unavailable"))
	}
	if real, err := filepath.EvalSymlinks(p.Path); err != nil || real != p.Path {
		return hold(errors.New("Git scope is an alias"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, err := retirementGit(ctx, p.Path, "rev-parse", "--show-toplevel")
	if err != nil || root != p.Path {
		return hold(errors.New("Git root differs"))
	}
	common, err := retirementGit(ctx, p.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common != p.GitCommonDir {
		return hold(errors.New("common Git authority differs"))
	}
	kind, err := retirementGit(ctx, p.Path, "cat-file", "-t", p.SnapshotTree)
	if err != nil || kind != "tree" {
		return hold(errors.New("durable Git recovery tree unavailable"))
	}
	key := "leases/" + filepath.Base(s.leasePath(p.Path))
	previous, err := s.database.Get(key)
	if err != nil {
		return hold(err)
	}
	// A lost response after the journaled CAS must not strand the successor:
	// the same pinned plan replays the untouched successor instead of HOLD.
	if out, done, err := s.replayRootSealed(p, previous); done {
		return out, err
	}
	digest := sha256.Sum256(previous)
	if hex.EncodeToString(digest[:]) != p.RecordSHA256 {
		return hold(errors.New("legacy record SHA changed"))
	}
	var old record
	if err := json.Unmarshal(previous, &old); err != nil {
		return hold(err)
	}
	if old.ProjectRoot != p.Path || old.Owner != p.Owner ||
		old.ExecutionID != p.ExecutionID || leaseIdentity(old) != p.LeaseID ||
		old.Generation != p.Generation || old.Generation == ^uint64(0) ||
		old.OwnerJobID != "" {
		return hold(errors.New("legacy holder identity changed or bound owner"))
	}
	if err := recoveryBoundOwner(old); err != nil {
		return hold(err)
	}
	gatePlan := p
	gatePlan.RecordSHA256 = ""
	gateBody, err := json.Marshal(gatePlan)
	if err != nil {
		return hold(err)
	}
	gateKey := s.writerGateKey(p.Path)
	created, err := s.database.PutIfAbsent(gateKey, gateBody)
	if err != nil {
		return hold(err)
	}
	if !created {
		existing, err := s.database.Get(gateKey)
		if err != nil || !bytes.Equal(existing, gateBody) {
			return hold(errors.New("writer gate differs"))
		}
	}
	if err := noSealedProcessReferences([]string{p.FenceRoot, p.GitCommonDir}); err != nil {
		return hold(err)
	}
	if err := rootSealedDirectory(p.FenceRoot); err != nil {
		return hold(err)
	}
	if err := rootSealedDirectory(p.GitCommonDir); err != nil {
		return hold(err)
	}
	// Re-pin the exact Git snapshot after the process fence is verified.
	// A preview alone cannot certify that HEAD and the worktree stayed put.
	currentTree, err := retirementGit(ctx, p.Path, "rev-parse", "HEAD^{tree}")
	if err != nil || currentTree != p.SnapshotTree {
		return hold(errors.New("Git HEAD tree changed after recovery preview"))
	}
	worktree, err := retirementGit(ctx, p.Path, "status", "--porcelain", "-uall")
	if err != nil {
		return hold(err)
	}
	snapshotRef := ""
	if worktree != "" || p.WorktreeSHA256 != "" {
		snapshotRef, err = s.preserveRecoveryWork(ctx, p)
		if err != nil {
			return hold(err)
		}
	}
	token, err := randomToken()
	if err != nil {
		return hold(err)
	}
	execution, err := randomToken()
	if err != nil {
		return hold(err)
	}
	now := time.Now().UTC()
	next := record{LeaseID: leaseIdentity(old), Generation: old.Generation + 1,
		ProjectRoot: p.Path, CommonGitDir: p.GitCommonDir, Owner: p.NewOwner, Token: token,
		ExecutionID: execution[:32], AcquiredAt: now, HeartbeatAt: now}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return hold(err)
	}
	nextRaw = append(nextRaw, '\n')
	journal := RootRecoveryJournal{Schema: "mcpshka.lease-recovery/v1",
		At: now.Format(time.RFC3339Nano), Path: p.Path,
		OldLeaseID: leaseIdentity(old), OldGeneration: old.Generation,
		NewGeneration: next.Generation, OldOwner: old.Owner, NewOwner: next.Owner,
		OldExecutionID: old.ExecutionID, NewExecutionID: next.ExecutionID,
		PreviousSHA256: p.RecordSHA256, SnapshotTree: p.SnapshotTree,
		Authorization: p.Authorization, SnapshotRef: snapshotRef, WorktreeSHA256: p.WorktreeSHA256}
	receipt, err := json.Marshal(journal)
	if err != nil {
		return hold(err)
	}
	ok, err := s.database.ReplaceWithJournal(key, previous, nextRaw,
		"project-lease-recoveries/v1/"+p.JournalID, receipt)
	if err != nil {
		return hold(fmt.Errorf("CAS outcome UNKNOWN; reconcile holder and journal: %w", err))
	}
	if !ok {
		return hold(errors.New("holder changed or audit journal already exists"))
	}
	// The operator must preserve the sealed OS boundary until every old
	// writer is independently fenced and the successor opens its pinned gate.
	return AcquireResult{Status: publicStatus(next), Token: token}, nil
}

// replayRootSealed reports done=true when this plan's journal already exists.
// It returns the current successor only while it is exactly the holder that
// journal installed; any later change stays HOLD and never re-rotates.
func (s *Service) replayRootSealed(p RootSealedRecovery, current []byte) (AcquireResult, bool, error) {
	raw, err := s.database.Get("project-lease-recoveries/v1/" + p.JournalID)
	if errors.Is(err, os.ErrNotExist) {
		return AcquireResult{}, false, nil
	}
	hold := func(reason string) (AcquireResult, bool, error) {
		return AcquireResult{}, true, fmt.Errorf("root-sealed legacy recovery HOLD: %s", reason)
	}
	if err != nil {
		return hold("recovery journal unavailable")
	}
	var j RootRecoveryJournal
	var next record
	if json.Unmarshal(raw, &j) != nil || json.Unmarshal(current, &next) != nil {
		return hold("recovery journal or holder unreadable")
	}
	if j.Schema != "mcpshka.lease-recovery/v1" || j.Path != p.Path || j.OldLeaseID != p.LeaseID ||
		j.OldGeneration != p.Generation || j.OldOwner != p.Owner || j.OldExecutionID != p.ExecutionID ||
		j.PreviousSHA256 != p.RecordSHA256 || j.SnapshotTree != p.SnapshotTree || j.WorktreeSHA256 != p.WorktreeSHA256 || j.NewOwner != p.NewOwner {
		return hold("journal belongs to a different recovery plan")
	}
	if next.ProjectRoot != p.Path || next.Owner != j.NewOwner || next.Generation != j.NewGeneration ||
		next.ExecutionID != j.NewExecutionID || leaseIdentity(next) != j.OldLeaseID || next.Token == "" || next.OwnerJobID != "" {
		return hold("recovery already completed and the successor has since changed")
	}
	return AcquireResult{Status: publicStatus(next), Token: next.Token}, true, nil
}

type RootSealedRecoveryPreview struct {
	Plan         RootSealedRecovery `json:"plan"`
	FencingState string             `json:"fencing_state"`
	Ready        bool               `json:"ready"`
	Reason       string             `json:"reason,omitempty"`
	Holders      []ProcessHolder    `json:"holders,omitempty"`
	// State separates why recovery may or may not proceed (P4): AUTH_OK —
	// Face ID passed and nothing blocks; OWNERSHIP_BLOCKED — live processes
	// or unbound identity evidence; DEPLOY_FENCED — a deployment is
	// unresolved; FENCE_REQUIRED — the independent OS fence is missing.
	State       string   `json:"state"`
	Blockers    []string `json:"blockers"`
	NextAction  string   `json:"next_action"`
	Disposition string   `json:"disposition"`
}

// WithState fills State from the preview's own evidence.
func (p RootSealedRecoveryPreview) WithState() RootSealedRecoveryPreview {
	reason := strings.ToLower(p.Reason)
	p.Blockers = []string{}
	p.Disposition = "blocker"
	p.NextAction = "project_lease_admin_recovery_preview"
	switch {
	case p.Ready:
		p.State = "AUTH_OK"
		p.Disposition = "ready"
		p.NextAction = "project_lease_admin_recover"
	case strings.Contains(reason, "bound owner"):
		p.State = "OWNERSHIP_BLOCKED"
		p.Blockers = append(p.Blockers, "owner_identity_unverified")
		p.NextAction = "project_lease_status"
	case strings.Contains(reason, "job"):
		p.State = "OWNERSHIP_BLOCKED"
		p.Blockers = append(p.Blockers, "project_jobs_unverified")
		p.NextAction = "project_context"
	case strings.Contains(reason, "deploy"):
		p.State = "DEPLOY_FENCED"
		p.Blockers = append(p.Blockers, "deployment_state_unverified")
		p.NextAction = "project_context"
	case len(p.Holders) > 0 || strings.Contains(reason, "process"):
		p.State = "OWNERSHIP_BLOCKED"
		p.Blockers = append(p.Blockers, "writer_processes_not_fenced")
		p.NextAction = "reconcile_reported_processes_without_signalling_unknown_owners"
	case strings.Contains(reason, "dirty") || strings.Contains(reason, "preserve"):
		p.State = "SNAPSHOT_REQUIRED"
		p.Blockers = append(p.Blockers, "durable_work_snapshot_required")
		p.NextAction = "preserve_work_with_current_holder_or_sealed_operator_snapshot"
	default:
		p.State = "FENCE_REQUIRED"
		p.Blockers = append(p.Blockers, "independent_os_fence_required")
		p.NextAction = "establish_root_owned_0500_source_and_common_git_fence"
	}
	return p
}

// PreviewRootSealedRecovery pins the existing legacy holder and a durable
// clean Git tree. It does not fence, authorize or alter the lease.
func (s *Service) PreviewRootSealedRecovery(path string) (RootSealedRecoveryPreview, error) {
	if s == nil || s.stateErr != nil || s.database == nil {
		return RootSealedRecoveryPreview{}, errors.New("SQLite lease authority required")
	}
	root, err := s.projectRoot(path)
	if err != nil {
		return RootSealedRecoveryPreview{}, err
	}
	r, err := s.readRecord(s.leasePath(root))
	if err != nil {
		return RootSealedRecoveryPreview{}, err
	}
	if r.OwnerJobID != "" || (r.ExecutionID != "" && !validExecutionID(r.ExecutionID)) {
		return RootSealedRecoveryPreview{}, errors.New("legacy recovery HOLD: expected unbound execution identity")
	}
	raw, err := s.database.Get("leases/" + filepath.Base(s.leasePath(root)))
	if err != nil {
		return RootSealedRecoveryPreview{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	unready := func(reason string) (RootSealedRecoveryPreview, error) {
		return (RootSealedRecoveryPreview{FencingState: "not_verified", Reason: reason}).WithState(), nil
	}
	if err := recoveryBoundOwner(r); err != nil {
		return unready(err.Error())
	}
	if err := s.recoveryQuiescent(root); err != nil {
		return unready(err.Error())
	}
	if err := rootSealedDirectory(root); err != nil {
		return unready("source must be root-owned and sealed 0500 by an independent operator")
	}
	common, err := retirementGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return RootSealedRecoveryPreview{}, err
	}
	if err := rootSealedDirectory(common); err != nil {
		return unready("common Git authority must be root-owned and sealed 0500")
	}
	holders, err := sealedProcessHolders([]string{root, common})
	if err != nil {
		return unready("incomplete process evidence: HOLD")
	}
	if len(holders) > 0 {
		out, err := unready("live process references: HOLD; " + summarizeHolders(holders))
		out.Holders = holders
		return out, err
	}
	clean, err := retirementGit(ctx, root, "status", "--porcelain", "-uall")
	if err != nil {
		return unready("Git state unavailable; preserve work")
	}
	worktreeDigest := ""
	if clean != "" {
		worktreeDigest, err = recoveryDigest(ctx, root)
		if err != nil {
			return unready("dirty source requires durable snapshot: " + err.Error())
		}
	}
	tree, err := retirementGit(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil || !retirementHex(tree, 40) {
		return RootSealedRecoveryPreview{}, errors.New("durable Git tree unavailable")
	}
	nonce, err := randomToken()
	if err != nil {
		return RootSealedRecoveryPreview{}, err
	}
	idSum := sha256.Sum256([]byte(nonce))
	id := hex.EncodeToString(idSum[:])
	sum := sha256.Sum256(raw)
	p := RootSealedRecovery{RootSealedRetirement: RootSealedRetirement{
		Path: root, FenceRoot: root, GitCommonDir: common,
		LeaseID: leaseIdentity(r), Generation: r.Generation, ExecutionID: r.ExecutionID,
		Owner: r.Owner, RecordSHA256: hex.EncodeToString(sum[:]),
		SnapshotTree: tree, JournalID: id, WorktreeSHA256: worktreeDigest,
	}, NewOwner: "operator-root:mcpshka-one-tree-" + id[:16]}
	result := RootSealedRecoveryPreview{Plan: p, FencingState: "not_verified"}
	if err := rootSealedDirectory(p.FenceRoot); err != nil {
		result.Reason = "source must be root-owned and sealed 0500 by an independent operator"
		return result, nil
	}
	if err := rootSealedDirectory(p.GitCommonDir); err != nil {
		result.Reason = "common Git authority must be root-owned and sealed 0500"
		return result, nil
	}
	if err := noSealedProcessReferences([]string{p.FenceRoot, p.GitCommonDir}); err != nil {
		result.Reason = "live process references or incomplete process evidence: HOLD"
		return result, nil
	}
	result.FencingState, result.Ready = "verified_sealed", true
	return result.WithState(), nil
}
