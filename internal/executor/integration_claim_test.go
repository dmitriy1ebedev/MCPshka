package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestIntegrationClaimSurvivesRestartAndRejectsOtherWriter(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	review := IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := c.reserveIntegrationClaim(ctx, a)
	if err != nil || claim.Generation != 1 {
		t.Fatalf("claim admission: %+v %v", claim, err)
	}
	if err := c.beginIntegrationEffect(ctx, claim); err != nil {
		t.Fatal(err)
	}
	cold := New(c.db)
	recovered, err := cold.reserveIntegrationClaim(ctx, a)
	if err != nil || recovered.State != "EFFECT_INTENT" || recovered.Generation != claim.Generation {
		t.Fatalf("cold intent lost: %+v %v", recovered, err)
	}
	// Another approved candidate shares the same common repository. Neither age
	// nor a cold controller can replace its unresolved integration reservation.
	other := item
	other.Request.ID = strings.Repeat("8", 32)
	other.ExecutionID = strings.Repeat("9", 32)
	b, err := c.recordIntegrationApproval(ctx, m, other, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cold.reserveIntegrationClaim(ctx, b); !errors.Is(err, ErrHold) {
		t.Fatal("unresolved writer replaced", err)
	}
	stale := claim
	stale.Generation++
	if err := cold.beginIntegrationEffect(ctx, stale); !errors.Is(err, ErrHold) {
		t.Fatal("wrong generation admitted", err)
	}
	if err := cold.finishIntegrationClaim(ctx, claim, integrationObservation{}); !errors.Is(err, ErrHold) {
		t.Fatal("unverified effect released writer", err)
	}
	if err := cold.finishIntegrationClaim(ctx, claim, integrationObservation{Repository: repo, Head: item.Request.Commit, Receipt: item.Request.Commit}); err != nil {
		t.Fatal(err)
	}
	next, err := cold.reserveIntegrationClaim(ctx, b)
	if err != nil || next.Generation != 2 {
		t.Fatalf("generation did not advance: %+v %v", next, err)
	}
	if err := cold.beginIntegrationEffect(ctx, claim); !errors.Is(err, ErrHold) {
		t.Fatal("ABA stale claim admitted", err)
	}
}

func TestIntegrationClaimConcurrentAdmissionHasOneWriter(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	review := IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	other := item
	other.Request.ID = strings.Repeat("8", 32)
	other.ExecutionID = strings.Repeat("9", 32)
	b, err := c.recordIntegrationApproval(ctx, m, other, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, approval := range []IntegrationApproval{a, b} {
		wg.Add(1)
		go func(a IntegrationApproval) {
			defer wg.Done()
			_, err := New(c.db).reserveIntegrationClaim(ctx, a)
			outcomes <- err
		}(approval)
	}
	wg.Wait()
	close(outcomes)
	accepted := 0
	for err := range outcomes {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrHold) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d writers", accepted)
	}
}

func TestCompletedIntegrationExecutionCannotBeReclaimedAfterAnotherCandidate(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	review := IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	other := item
	other.Request.ID = strings.Repeat("8", 32)
	other.ExecutionID = strings.Repeat("9", 32)
	b, err := c.recordIntegrationApproval(ctx, m, other, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	var first IntegrationClaim
	for _, approved := range []IntegrationApproval{a, b} {
		claim, err := c.reserveIntegrationClaim(ctx, approved)
		if err != nil {
			t.Fatal(err)
		}
		if approved.ID == a.ID {
			first = claim
		}
		if err := c.beginIntegrationEffect(ctx, claim); err != nil {
			t.Fatal(err)
		}
		if err := c.finishIntegrationClaim(ctx, claim, integrationObservation{Repository: repo, Head: approved.Request.Commit, Receipt: approved.Request.Commit}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(c.db).reserveIntegrationClaim(ctx, a)
	if err != nil || got.State != "RELEASED" || got.Generation != first.Generation {
		t.Fatalf("finished execution admitted again after another generation: %+v %v", got, err)
	}
	if err := c.beginIntegrationEffect(ctx, got); !errors.Is(err, ErrHold) {
		t.Fatal("completed execution got another effect", err)
	}
}

func TestIntegrationClaimReleasedWithoutCompletionRemainsHold(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := c.reserveIntegrationClaim(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.beginIntegrationEffect(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := c.finishIntegrationClaim(ctx, claim, integrationObservation{Repository: repo, Head: a.Request.Commit, Receipt: a.Request.Commit}); err != nil {
		t.Fatal(err)
	}
	raw, err := c.db.GetContext(ctx, integrationCompletionKey(claim.ExecutionID))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.db.Delete(integrationCompletionKey(claim.ExecutionID), raw); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := New(c.db).reserveIntegrationClaim(ctx, a); !errors.Is(err, ErrHold) {
		t.Fatal("missing durable completion accepted as released", err)
	}
	other := item
	other.Request.ID = strings.Repeat("8", 32)
	other.ExecutionID = strings.Repeat("9", 32)
	b, err := c.recordIntegrationApproval(ctx, m, other, repo, a.Review)
	if err != nil {
		t.Fatal(err)
	}
	observation := integrationObservation{Repository: repo, Head: a.Request.Commit, Receipt: a.Request.Commit}
	checkHold := func() {
		t.Helper()
		if _, err := New(c.db).reserveIntegrationClaim(ctx, a); !errors.Is(err, ErrHold) {
			t.Fatal("corrupt completion accepted on replay", err)
		}
		if _, err := New(c.db).reserveIntegrationClaim(ctx, b); !errors.Is(err, ErrHold) {
			t.Fatal("corrupt completion overwritten by next execution", err)
		}
		if err := New(c.db).finishIntegrationClaim(ctx, claim, observation); !errors.Is(err, ErrHold) {
			t.Fatal("corrupt completion accepted on repeated finish", err)
		}
	}
	checkHold()
	conflicting := claim
	conflicting.State = "RELEASED"
	conflicting.Generation++
	conflictingRaw, _ := json.Marshal(conflicting)
	for _, broken := range [][]byte{[]byte("{malformed"), conflictingRaw} {
		if ok, err := c.db.PutIfAbsent(integrationCompletionKey(claim.ExecutionID), broken); err != nil || !ok {
			t.Fatal("install isolated corruption", err)
		}
		checkHold()
		if ok, err := c.db.Delete(integrationCompletionKey(claim.ExecutionID), broken); err != nil || !ok {
			t.Fatal(err)
		}
	}
}

func TestIntegrationClaimRecoversCompletionBeforeReservationRelease(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := c.reserveIntegrationClaim(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.beginIntegrationEffect(ctx, claim); err != nil {
		t.Fatal(err)
	}
	_, intent, err := c.readIntegrationClaim(ctx, repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	observation := integrationObservation{Repository: repo, Head: a.Request.Commit, Receipt: a.Request.Commit}
	if err := c.finishIntegrationClaim(ctx, claim, observation); err != nil {
		t.Fatal(err)
	}
	_, released, err := c.readIntegrationClaim(ctx, repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	// Isolated database fault: durable completion survived, but retirement of
	// the shared reservation did not. Never simulate this against live state.
	if ok, err := c.db.Replace(integrationClaimKey(repo.CommonDir), released, intent); err != nil || !ok {
		t.Fatal(err)
	}
	cold := New(c.db)
	recovered, err := cold.reserveIntegrationClaim(ctx, a)
	if err != nil || recovered.State != "EFFECT_INTENT" || recovered.Generation != claim.Generation {
		t.Fatalf("lost uncertain effect: %+v %v", recovered, err)
	}
	if err := cold.beginIntegrationEffect(ctx, recovered); !errors.Is(err, ErrHold) {
		t.Fatal("uncertain effect replay permitted", err)
	}
	if err := cold.finishIntegrationClaim(ctx, recovered, observation); err != nil {
		t.Fatal(err)
	}
	final, err := cold.reserveIntegrationClaim(ctx, a)
	if err != nil || final.State != "RELEASED" || final.Generation != claim.Generation {
		t.Fatalf("retirement reconciliation failed: %+v %v", final, err)
	}
}
