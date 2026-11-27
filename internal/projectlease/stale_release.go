package projectlease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/agents"
)

// A holder bound to a local process is gone once that exact incarnation is
// (PID, start ticks, boot). A remote chat holder has no process here, and
// MCPshka is its only write path: deleting the record fences its token, so
// it is released under the owner's Face ID once it has been silent for
// RemoteHolderQuiet or its agent_task unit has ended, with no project job,
// deployment or process in the worktree. Uncommitted files stay in place.
// Requiring a PID proof for remote chats made their leases unreleasable;
// the old 2 h wait stalled DIAG for hours (BUG-20261026-135).
const RemoteHolderQuiet = 30 * time.Minute

// endedAgentHolder proves the holder was an agent_task that has ended: its
// record covers the lease and its unit is no longer active. Swapped by tests.
var endedAgentHolder = func(root, owner string, acquired, last time.Time) bool {
	id, ok := strings.CutPrefix(owner, "agent-task:")
	if !ok || !agents.ValidTaskID(id) {
		return false
	}
	t, err := agents.ReadTask(id)
	if err != nil || t.Path != root || t.StartedAt.IsZero() || t.StartedAt.After(acquired) || t.FinishedAt.IsZero() || t.FinishedAt.Before(last) || t.State == "running" || t.State == "QUEUED" {
		return false
	}
	return agents.TaskUnitsEnded(t.ID)
}

type StaleReleasePlan struct {
	Path          string    `json:"path"`
	LeaseID       string    `json:"lease_id"`
	Generation    uint64    `json:"generation"`
	Owner         string    `json:"owner"`
	ExecutionID   string    `json:"execution_id,omitempty"`
	RecordSHA256  string    `json:"record_sha256"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// DeadScratchClone: old agents leased scratch clones /tmp/mcpshka-NAME;
// once the clone is gone its lease is dead and may be released (MCP-167).
func DeadScratchClone(path string) bool {
	name, ok := strings.CutPrefix(path, "/tmp/mcpshka-")
	if !ok || name == "" || len(name) > 96 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return false
	}
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

// staleReleasePath preserves the exact lease scope even after a worktree is
// removed; resolving Git here could select its parent's unrelated lease.
func (s *Service) staleReleasePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 || len(strings.Split(path, "/")) > 32 {
		return errors.New("noncanonical or over-budget stale lease scope: HOLD")
	}
	// Inspect every existing prefix, including ancestors of the allowed root.
	// A dangling symlink must never be mistaken for an absent directory.
	prefix := "/"
	existing := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		prefix = filepath.Join(prefix, part)
		info, err := os.Lstat(prefix)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return fmt.Errorf("stale lease directory unavailable: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("stale lease scope contains an alias or nondirectory: HOLD")
		}
		existing = prefix
	}
	real, err := s.policy.ResolveExisting(existing)
	if err != nil || real != existing {
		return errors.New("stale lease scope is aliased or outside allowed roots: HOLD")
	}
	return nil
}

func (s *Service) staleEvidence(path string, now time.Time) (StaleReleasePlan, []byte, error) {
	if os.Geteuid() != 0 || s == nil || s.database == nil || s.stateErr != nil {
		return StaleReleasePlan{}, nil, errors.New("trusted root SQLite operator required")
	}
	root := path
	if err := s.staleReleasePath(path); err != nil {
		return StaleReleasePlan{}, nil, err
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, err := s.database.Get(key)
	if err != nil {
		return StaleReleasePlan{}, nil, fmt.Errorf("no lease to release: %w", err)
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return StaleReleasePlan{}, nil, err
	}
	if r.ProjectRoot != root || r.Owner == "" || r.Token == "" || r.AcquiredAt.IsZero() {
		return StaleReleasePlan{}, nil, errors.New("lease record does not own the exact scope: HOLD")
	}
	last := r.AcquiredAt
	if r.HeartbeatAt.After(last) {
		last = r.HeartbeatAt
	}
	if r.OwnerPID != 0 || r.OwnerStartTicks != 0 || r.OwnerBootID != "" {
		if err := recoveryBoundOwner(r); err != nil {
			return StaleReleasePlan{}, nil, err
		}
	} else if silent := now.Sub(last); silent < RemoteHolderQuiet && !endedAgentHolder(root, r.Owner, r.AcquiredAt, last) {
		return StaleReleasePlan{}, nil, recoveryHold("remote_holder_recently_active", root, fmt.Sprintf("remote holder heartbeat %s ago, under %s: it may still be working; retry after the quiet period", silent.Round(time.Second), RemoteHolderQuiet), nil)
	}
	if err := s.recoveryQuiescent(root); err != nil {
		return StaleReleasePlan{}, nil, err
	}
	holders, err := processHolders([]string{root, root + " (deleted)"}, false)
	if err != nil {
		return StaleReleasePlan{}, nil, errors.New("incomplete process evidence: HOLD")
	}
	if len(holders) > 0 {
		return StaleReleasePlan{}, nil, errors.New("live processes use the worktree: HOLD; " + summarizeHolders(holders))
	}
	sum := sha256.Sum256(raw)
	return StaleReleasePlan{Path: root, LeaseID: leaseIdentity(r), Owner: r.Owner, Generation: r.Generation, ExecutionID: r.ExecutionID, RecordSHA256: hex.EncodeToString(sum[:]), LastHeartbeat: last}, raw, nil
}

// PreviewStaleRelease returns the pinned plan when the holder is provably gone.
func (s *Service) PreviewStaleRelease(path string, now time.Time) (StaleReleasePlan, error) {
	p, _, err := s.staleEvidence(path, now)
	return p, err
}

// ReleaseStale re-proves the evidence and deletes exactly the pinned record.
func (s *Service) ReleaseStale(p StaleReleasePlan, now time.Time) (ReleaseResult, error) {
	if s == nil {
		return ReleaseResult{}, errors.New("lease service required")
	}
	s.interactiveMu.Lock()
	defer s.interactiveMu.Unlock()
	if s.database == nil || s.stateErr != nil || os.Geteuid() != 0 || !retirementHex(p.RecordSHA256, 64) {
		return ReleaseResult{}, errors.New("trusted root SQLite recovery required")
	}
	journalKey := "project-stale-releases/v1/" + p.RecordSHA256
	receipt, err := json.Marshal(p)
	if err != nil {
		return ReleaseResult{}, err
	}
	if previous, err := s.database.Get(journalKey); err == nil {
		if string(previous) != string(receipt) {
			return ReleaseResult{}, errors.New("stale recovery receipt mismatch: HOLD")
		}
		return ReleaseResult{ProjectRoot: p.Path, Released: true}, nil
	} else if !os.IsNotExist(err) {
		return ReleaseResult{}, err
	}
	// Prove first (the fence lock is itself an open file), then take the fence
	// and delete by CAS on exactly the proven bytes: any acquire, heartbeat or
	// transfer in between changes the record and the delete fails.
	live, raw, err := s.staleEvidence(p.Path, now)
	if err != nil {
		return ReleaseResult{}, err
	}
	if live != p {
		return ReleaseResult{}, recoveryHold("STALE_RECOVERY_PLAN", p.Path, "lease changed since preview; request a fresh preview", nil)
	}
	fence, err := s.lockInteractiveFence(p.Path)
	if err != nil {
		return ReleaseResult{}, err
	}
	defer fence.Close()
	if err := s.staleReleasePath(p.Path); err != nil {
		return ReleaseResult{}, err
	}
	ok, err := s.database.DeleteWithJournal("leases/"+filepath.Base(s.leasePath(p.Path)), raw, journalKey, receipt)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("stale lease release outcome UNKNOWN; reconcile holder and audit journal: %w", err)
	}
	if !ok {
		return ReleaseResult{}, recoveryHold("STALE_RECOVERY_PLAN", p.Path, "lease changed during release; request a fresh preview", nil)
	}
	return ReleaseResult{ProjectRoot: p.Path, Released: true}, nil
}
