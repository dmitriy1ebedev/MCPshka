package projectlease

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func testService(t *testing.T, root string) *Service {
	t.Helper()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	return New(filepath.Join(root, ".lease-state"), p)
}

func TestAdministrativeReviewDoesNotChangeWriter(t *testing.T) {
	root := t.TempDir()
	s := testService(t, root)
	held, err := s.AcquireTracked(root, "codex", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.ReviewAdministrativeTransfer(root)
	if err != nil {
		t.Fatal(err)
	}
	if before.Owner != "codex" || before.ExecutionID != held.Status.ExecutionID || !before.RequiresAdministrator || before.OwnerBound {
		t.Fatalf("unsafe review result: %+v", before)
	}
	after, err := s.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Held || after.Owner != "codex" || after.ExecutionID != held.Status.ExecutionID {
		t.Fatalf("review mutated writer: %+v", after)
	}
	if _, err := s.Release(root, held.Token); err != nil {
		t.Fatal(err)
	}
}

func TestAdministrativeTransferHoldsWithoutWriterFencing(t *testing.T) {
	root := t.TempDir()
	if _, err := statestore.Open(filepath.Join(root, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := testService(t, root)
	old, err := s.AcquireTracked(root, "chatgpt-diag-p02-20261009", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	proof := TransferProof{ExpectedOwner: old.Status.Owner, ExpectedExecutionID: old.Status.ExecutionID, Authorized: true, JobsQuiescent: true, ProcessesQuiescent: true, DeploymentQuiescent: true, AuditRecorded: true}
	if _, err := s.TransferAdministrative(root, "codex", proof); err == nil {
		t.Fatal("unbound writer transferred without enforceable fencing")
	}
	if _, err := s.ValidateHolder(root, old.Token); err != nil {
		t.Fatal("HOLD changed owner:", err)
	}
}

func TestAdministrativeReviewNeverClaimsLegacyWriterIsFenced(t *testing.T) {
	root := t.TempDir()
	s, root := heartbeatSQLiteService(t)
	held, err := s.AcquireTracked(root, "legacy-owner", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.ReviewAdministrativeTransfer(root)
	if err != nil {
		t.Fatal(err)
	}
	if review.OwnerBound || review.FencingState != "not_verified" || !review.RequiresAdministrator {
		t.Fatalf("unbound owner was treated as safely transferable: %+v", review)
	}
	if review.LeaseID != held.Status.LeaseID || review.Generation != held.Status.Generation {
		t.Fatalf("review lost pinned lease identity: %+v", review)
	}
	if _, err := s.ValidateHolder(root, held.Token); err != nil {
		t.Fatalf("read-only review invalidated current holder: %v", err)
	}
}

func TestAdministrativeReviewReleasedScope(t *testing.T) {
	root := t.TempDir()
	s := testService(t, root)
	review, err := s.ReviewAdministrativeTransfer(root)
	if err != nil || review.State != "RELEASED" {
		t.Fatalf("released review: %+v %v", review, err)
	}
}

func TestLeaseAcquireStatusRelease(t *testing.T) {
	root := t.TempDir()
	s := testService(t, root)

	got, err := s.Acquire(root, "chat-a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Status.Held || got.Status.Owner != "chat-a" || got.Token == "" {
		t.Fatalf("unexpected acquire result: %+v", got)
	}
	status, err := s.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Held || status.Owner != "chat-a" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if _, err := s.Release(root, "wrong-token"); err == nil {
		t.Fatal("wrong token released lease")
	}
	if _, err := s.Release(root, got.Token); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Held {
		t.Fatalf("lease still held: %+v", status)
	}
}

func TestLeaseCanonicalizesGitSubdirectory(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	cmd.Env = policy.MinimalEnv(root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	sub := filepath.Join(root, "internal", "x")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	s := testService(t, root)
	first, err := s.Acquire(sub, "chat-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status.ProjectRoot != root {
		t.Fatalf("root=%q want %q", first.Status.ProjectRoot, root)
	}
	if _, err := s.Acquire(root, "chat-b"); err == nil {
		t.Fatal("same repository acquired through a second path")
	}
	if _, err := s.Release(root, first.Token); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseIsAtomicAcrossServiceInstances(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".lease-state")
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}

	const contenders = 12
	var wg sync.WaitGroup
	results := make(chan AcquireResult, contenders)
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := New(dir, p).Acquire(root, "contender")
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	var winners []AcquireResult
	for got := range results {
		winners = append(winners, got)
	}
	if len(winners) != 1 {
		t.Fatalf("winners=%d want 1", len(winners))
	}
	if len(errs) != contenders-1 {
		t.Fatalf("errors=%d want %d", len(errs), contenders-1)
	}
	if _, err := New(dir, p).Release(root, winners[0].Token); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyHeartbeatFailsClosedAndStatusNeverReclaims(t *testing.T) {
	root := t.TempDir()
	s := testService(t, root)
	held, err := s.Acquire(root, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(root, held.Token); err == nil {
		t.Fatal("legacy heartbeat must fail without cross-process CAS")
	}
	r := record{ProjectRoot: root, Owner: "owner", Token: held.Token, AcquiredAt: time.Now().UTC().Add(-time.Hour)}
	status := publicStatusAt(r, time.Now().UTC())
	if !status.Held || !status.ReconciliationRequired || status.Reclaimable || status.OwnerLiveness != "unverified" {
		t.Fatalf("old lease should require owner reconciliation, not takeover: %+v", status)
	}
	future := r
	future.AcquiredAt = time.Now().UTC().Add(10 * time.Minute)
	if got := publicStatusAt(future, time.Now().UTC()); !got.ReconciliationRequired || got.Reclaimable {
		t.Fatalf("future timestamp should fail closed: %+v", got)
	}
	if _, err := s.Release(root, held.Token); err != nil {
		t.Fatal(err)
	}
}

func TestValidateHolderRejectsReleasedAndReplacedReceipts(t *testing.T) {
	root := t.TempDir()
	s := testService(t, root)
	first, err := s.Acquire(root, "same-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateHolder(root, first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateHolder(root, "wrong"); err == nil {
		t.Fatal("accepted wrong token")
	}
	if _, err := s.Release(root, first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateHolder(root, first.Token); err == nil {
		t.Fatal("accepted released receipt")
	}
	second, err := s.Acquire(root, "same-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateHolder(root, first.Token); err == nil {
		t.Fatal("accepted previous token for same owner")
	}
	if _, err := s.ValidateHolder(root, second.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(root, second.Token); err != nil {
		t.Fatal(err)
	}
}

func TestAdministrativeTransferRejectsChangedOwnerBeforeFencing(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	old, err := s.AcquireTracked(root, "current-owner", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	fenceCalls := 0
	proof := TransferProof{
		ExpectedOwner: "outdated-owner", ExpectedExecutionID: old.Status.ExecutionID,
		Authorized: true, JobsQuiescent: true, ProcessesQuiescent: true,
		DeploymentQuiescent: true, AuditRecorded: true,
		writerFence: func(action func() error) error { fenceCalls++; return action() },
	}
	if _, err := s.TransferAdministrative(root, "next-owner", proof); err == nil {
		t.Fatal("changed owner accepted")
	}
	if fenceCalls != 0 {
		t.Fatal("fenced a writer whose identity did not match the approved transfer")
	}
	if _, err := s.ValidateHolder(root, old.Token); err != nil {
		t.Fatal(err)
	}
}

func TestLeasePublicIdentitySurvivesHeartbeatAndChangesOnReacquire(t *testing.T) {
	s, root := heartbeatSQLiteService(t)
	first, err := s.AcquireTracked(root, "owner", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(first.Status)
	var identity map[string]any
	if err := json.Unmarshal(raw, &identity); err != nil {
		t.Fatal(err)
	}
	id, ok := identity["lease_id"].(string)
	if !ok || len(id) != 32 || identity["generation"] != float64(1) {
		t.Fatal("new lease lacks pinned public identity and generation")
	}
	renewed, err := s.HeartbeatRefresh(root, first.Token)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(renewed)
	var after map[string]any
	_ = json.Unmarshal(raw, &after)
	if after["lease_id"] != id || after["generation"] != float64(1) {
		t.Fatal("heartbeat changed lease identity")
	}
	if _, err := s.Release(root, first.Token); err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireTracked(root, "owner", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(second.Status)
	_ = json.Unmarshal(raw, &after)
	if after["lease_id"] == id {
		t.Fatal("same owner and execution resurrected the previous lease identity")
	}
}

func boundTransferFixture(t *testing.T) (*Service, string, AcquireResult) {
	t.Helper()
	s, root := heartbeatSQLiteService(t)
	old, err := s.AcquireTracked(root, "workflow:0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, err := s.database.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	r.OwnerJobID = "workflow-" + r.ExecutionID
	r.OwnerPID, r.OwnerStartTicks, r.OwnerBootID = 123, 456, "fixture-boot"
	raw, _ = json.Marshal(r)
	if err := s.database.Put(key, raw); err != nil {
		t.Fatal(err)
	}
	old.Status, err = s.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root, old
}

func transferProofFor(old AcquireResult) TransferProof {
	return TransferProof{
		ExpectedOwner: old.Status.Owner, ExpectedExecutionID: old.Status.ExecutionID,
		ExpectedLeaseID: old.Status.LeaseID, ExpectedGeneration: old.Status.Generation,
		Authorized: true, JobsQuiescent: true, ProcessesQuiescent: true,
		DeploymentQuiescent: true, AuditRecorded: true,
	}
}

func TestAdministrativeTransferCommitsWithinFenceAndInvalidatesOldCredentials(t *testing.T) {
	s, root, old := boundTransferFixture(t)
	proof := transferProofFor(old)
	proof.writerFence = func(commit func() error) error {
		if _, err := s.ValidateHolder(root, old.Token); err != nil {
			return err
		}
		if err := commit(); err != nil {
			return err
		}
		inside, err := s.Status(root)
		if err != nil {
			return err
		}
		if inside.Owner != "next-owner" || inside.Generation != 2 {
			t.Fatal("writer fence ended before ownership CAS")
		}
		return nil
	}
	next, err := s.TransferAdministrative(root, "next-owner", proof)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status.LeaseID != old.Status.LeaseID || next.Status.Generation != 2 ||
		next.Status.ExecutionID == old.Status.ExecutionID || next.Token == old.Token {
		t.Fatal("handoff did not preserve lease ID and advance holder credentials")
	}
	if _, err := s.HeartbeatRefresh(root, old.Token); err == nil {
		t.Fatal("old holder renewed new lease")
	}
	if _, err := s.Release(root, old.Token); err == nil {
		t.Fatal("old holder released new lease")
	}
	if _, err := s.ValidateHolder(root, next.Token); err != nil {
		t.Fatal(err)
	}
	calls := 0
	proof.writerFence = func(commit func() error) error { calls++; return commit() }
	if _, err := s.TransferAdministrative(root, "third-owner", proof); err == nil || calls != 0 {
		t.Fatal("stale transfer reached fencing")
	}
}

func TestAdministrativeTransferRejectsABAAndGenerationBeforeFencing(t *testing.T) {
	for _, change := range []string{"generation", "reacquire", "unbound"} {
		t.Run(change, func(t *testing.T) {
			s, root, old := boundTransferFixture(t)
			proof := transferProofFor(old)
			calls := 0
			proof.writerFence = func(commit func() error) error { calls++; return commit() }
			switch change {
			case "generation":
				proof.ExpectedGeneration++
			case "reacquire":
				if _, err := s.Release(root, old.Token); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AcquireTracked(root, old.Status.Owner, old.Status.ExecutionID); err != nil {
					t.Fatal(err)
				}
			case "unbound":
				key := "leases/" + filepath.Base(s.leasePath(root))
				raw, _ := s.database.Get(key)
				var r record
				_ = json.Unmarshal(raw, &r)
				r.OwnerPID = 0
				raw, _ = json.Marshal(r)
				if err := s.database.Put(key, raw); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.TransferAdministrative(root, "next-owner", proof); err == nil || calls != 0 {
				t.Fatal("unsafe request reached writer fence")
			}
		})
	}
}

func TestAdministrativeTransferCASRejectsHeartbeatRace(t *testing.T) {
	s, root, old := boundTransferFixture(t)
	proof := transferProofFor(old)
	proof.writerFence = func(commit func() error) error {
		if _, err := s.HeartbeatRefresh(root, old.Token); err != nil {
			return err
		}
		return commit()
	}
	if _, err := s.TransferAdministrative(root, "next-owner", proof); err == nil {
		t.Fatal("transfer ignored an authority change during reconciliation")
	}
	if _, err := s.ValidateHolder(root, old.Token); err != nil {
		t.Fatal("CAS race changed ownership", err)
	}
}

func TestAdministrativeTransferFenceMustInvokeCommit(t *testing.T) {
	s, root, old := boundTransferFixture(t)
	proof := transferProofFor(old)
	proof.writerFence = func(commit func() error) error { return nil }
	if _, err := s.TransferAdministrative(root, "next-owner", proof); err == nil || !strings.Contains(err.Error(), "fence did not commit") {
		t.Fatalf("empty fencing callback must HOLD, got %v", err)
	}
	if _, err := s.ValidateHolder(root, old.Token); err != nil {
		t.Fatalf("failed fence invalidated original writer: %v", err)
	}
}

func TestAdministrativeTransferRejectsDoubleCommitWithoutReplayingCAS(t *testing.T) {
	s, root, old := boundTransferFixture(t)
	proof := transferProofFor(old)
	proof.writerFence = func(commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		return commit()
	}
	_, err := s.TransferAdministrative(root, "next-owner", proof)
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("repeated callback after CAS must be UNKNOWN, got %v", err)
	}
	status, err := s.Status(root)
	if err != nil || status.Generation != old.Status.Generation+1 || status.Owner != "next-owner" {
		t.Fatalf("double callback changed committed writer identity: %+v %v", status, err)
	}
	if _, err := s.ValidateHolder(root, old.Token); err == nil {
		t.Fatal("stale owner retained authority after committed transfer")
	}
}

func TestAdministrativeTransferRejectsDeferredCommitOutsideFence(t *testing.T) {
	s, root, old := boundTransferFixture(t)
	proof := transferProofFor(old)
	var deferred func() error
	proof.writerFence = func(commit func() error) error {
		deferred = commit
		return nil
	}
	if _, err := s.TransferAdministrative(root, "next-owner", proof); err == nil || !strings.Contains(err.Error(), "fence did not commit") {
		t.Fatalf("deferred callback must HOLD: %v", err)
	}
	if deferred == nil {
		t.Fatal("missing captured callback")
	}
	if err := deferred(); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("late callback must not commit: %v", err)
	}
	if _, err := s.ValidateHolder(root, old.Token); err != nil {
		t.Fatalf("late callback changed original authority: %v", err)
	}
}

func TestAdministrativeTransferPostCommitFailureIsUnknown(t *testing.T) {
	s, root, old := boundTransferFixture(t)
	proof := transferProofFor(old)
	proof.writerFence = func(commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		return errors.New("fence finalization unavailable")
	}
	if _, err := s.TransferAdministrative(root, "next-owner", proof); err == nil || !strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatal("post-commit failure was not classified UNKNOWN")
	}
	status, err := s.Status(root)
	if err != nil || status.Owner != "next-owner" || status.Generation != 2 {
		t.Fatal("lost committed authority", err)
	}
	if _, err := s.ValidateHolder(root, old.Token); err == nil {
		t.Fatal("old authority resurrected")
	}
}

func TestHolderReconcileNamesStaleHeartbeat(t *testing.T) {
	live := Status{ExecutionID: strings.Repeat("a", 32), Generation: 1}
	if err := holderReconcile(live); err != nil {
		t.Fatal(err)
	}
	stale := live
	stale.ReconciliationRequired = true
	if err := holderReconcile(stale); err == nil || !strings.Contains(err.Error(), "project_lease_heartbeat") {
		t.Fatalf("stale own lease must ask for a heartbeat: %v", err)
	}
	job := stale
	job.OwnerJobID = "job-1"
	if err := holderReconcile(job); err == nil || !strings.Contains(err.Error(), "requires reconciliation") {
		t.Fatalf("job-bound holder must stay HOLD: %v", err)
	}
}

func TestRecoveryPreviewStatesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		p    RootSealedRecoveryPreview
		want string
	}{
		{RootSealedRecoveryPreview{Ready: true}, "AUTH_OK"},
		{RootSealedRecoveryPreview{Reason: "incomplete process evidence: HOLD"}, "OWNERSHIP_BLOCKED"},
		{RootSealedRecoveryPreview{Holders: []ProcessHolder{{}}}, "OWNERSHIP_BLOCKED"},
		{RootSealedRecoveryPreview{Reason: "unresolved deployment pending"}, "DEPLOY_FENCED"},
		{RootSealedRecoveryPreview{Reason: "source must be root-owned and sealed 0500"}, "FENCE_REQUIRED"},
	} {
		if got := tc.p.WithState().State; got != tc.want {
			t.Fatalf("%+v: %s, want %s", tc.p, got, tc.want)
		}
	}
}
