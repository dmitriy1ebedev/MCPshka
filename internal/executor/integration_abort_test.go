package executor

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/integrationqueue"
	"os"
	"strings"
	"testing"
)

type integrationAbortProbe struct {
	integrationRecoveryProbe
	noEffectErr error
	checked     int
}

func (p *integrationAbortProbe) ObserveNoEffect(context.Context, IntegrationApproval) error {
	if !p.fenced || p.stopped != 1 {
		return ErrHold
	}
	p.checked++
	return p.noEffectErr
}
func TestIntegrationAbortRequiresFreshExactFenceAndNoEffect(t *testing.T) {
	for _, kind := range []string{"not-fenced", "writer-barrier", "still-running", "effect-present", "success"} {
		t.Run(kind, func(t *testing.T) {
			c, a, claim, pin, launch := integrationSupervisorFixture(t)
			ctx := context.Background()
			// Simulate a failure before EFFECT_INTENT, with the real sealed binding.
			claim.State = "HELD"
			raw, _ := json.Marshal(claim)
			if e := c.db.Put(integrationClaimKey(claim.Repository.CommonDir), raw); e != nil {
				t.Fatal(e)
			}
			if kind != "not-fenced" {
				fenceIntegrationFixture(t, c, pin, launch)
			}
			probe := &integrationAbortProbe{}
			if kind == "writer-barrier" {
				probe.writerFenceErr = ErrHold
			}
			if kind == "still-running" {
				probe.stoppedErr = ErrHold
			}
			if kind == "effect-present" {
				probe.noEffectErr = ErrHold
			}
			err := c.abortIntegrationClaim(ctx, a, probe)
			if kind != "success" {
				if !errors.Is(err, ErrHold) {
					t.Fatal("unsafe abort accepted", err)
				}
				got, _, e := c.readIntegrationClaim(ctx, claim.Repository.CommonDir)
				if e != nil || got.State != "HELD" {
					t.Fatal("failed proof retired claim", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _, e := New(c.db).readIntegrationClaim(ctx, claim.Repository.CommonDir)
			if e != nil || got.State != "FAILED" {
				t.Fatal("not a failed terminal claim", e)
			}
			if got.State == "RELEASED" {
				t.Fatal("fabricated successful completion")
			}
			again, e := c.reserveIntegrationClaim(ctx, a)
			if e != nil || again.State != "FAILED" {
				t.Fatal("same execution was re-enabled", e)
			}
			if e := c.abortIntegrationClaim(ctx, a, probe); e != nil {
				t.Fatal("cold completed replay", e)
			}
		})
	}
}
func TestIntegrationAbortRecoversCompletionBeforeClaimCAS(t *testing.T) {
	c, a, claim, pin, launch := integrationSupervisorFixture(t)
	ctx := context.Background()
	claim.State = "HELD"
	raw, _ := json.Marshal(claim)
	if e := c.db.Put(integrationClaimKey(claim.Repository.CommonDir), raw); e != nil {
		t.Fatal(e)
	}
	failed := claim
	failed.State = "FAILED"
	raw, _ = json.Marshal(failed)
	if e := c.db.Put(integrationCompletionKey(claim.ExecutionID), raw); e != nil {
		t.Fatal(e)
	}
	fenceIntegrationFixture(t, c, pin, launch)
	if e := New(c.db).abortIntegrationClaim(ctx, a, &integrationAbortProbe{}); e != nil {
		t.Fatal(e)
	}
	got, _, e := c.readIntegrationClaim(ctx, claim.Repository.CommonDir)
	if e != nil || got.State != "FAILED" {
		t.Fatal(e)
	}
}
func TestProtectedAbortNoEffectPhysicalEvidence(t *testing.T) {
	for _, kind := range []string{"empty", "old-head", "advanced-head", "candidate-head", "effect-intent", "corrupt-head"} {
		t.Run(kind, func(t *testing.T) {
			c, parent, a, root := protectedInitialFixture(t)
			ctx := context.Background()
			switch kind {
			case "old-head":
				if e := publishProtectedRef(ctx, root, "refs/heads/main", a.Request.TargetHead, true); e != nil {
					t.Fatal(e)
				}
			case "advanced-head":
				if e := publishProtectedRef(ctx, root, "refs/heads/main", strings.Repeat("c", 40), true); e != nil {
					t.Fatal(e)
				}
			case "candidate-head":
				if e := publishProtectedRef(ctx, root, "refs/heads/main", a.Request.Commit, true); e != nil {
					t.Fatal(e)
				}
			case "effect-intent":
				if e := c.db.Put(protectedEffectKey(a.ExecutionID), []byte("{}")); e != nil {
					t.Fatal(e)
				}
			case "corrupt-head":
				if e := root.WriteFile("refs/heads/main", []byte("unknown"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			err := c.observeProtectedNoEffect(ctx, parent, a, uint32(os.Geteuid()))
			if kind == "empty" || kind == "old-head" || kind == "advanced-head" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrHold) {
				t.Fatal("ambiguous physical evidence aborted", err)
			}
		})
	}
}

func TestIntegrationAbortHistoricalFailedStatusAfterNewGeneration(t *testing.T) {
	c, a, _, pin, launch := integrationSupervisorFixture(t)
	ctx := context.Background()
	fenceIntegrationFixture(t, c, pin, launch)
	if e := c.abortIntegrationClaim(ctx, a, &integrationAbortProbe{}); e != nil {
		t.Fatal(e)
	}
	failed, e := c.integrationClaimForApproval(ctx, a)
	if e != nil || failed.State != "FAILED" {
		t.Fatal(e)
	}
	next := a
	next.ExecutionID = strings.Repeat("4", 32)
	if next.ExecutionID == a.ExecutionID {
		next.ExecutionID = strings.Repeat("3", 32)
	}
	item := integrationqueue.RuntimeItem{Request: next.Request, ExecutionID: next.ExecutionID, State: integrationqueue.Pending}
	next.ID = integrationApprovalID(item)
	raw, _ := json.Marshal(next)
	if e := c.db.Put("executor/integration-approvals/"+next.ID+".json", raw); e != nil {
		t.Fatal(e)
	}
	claim, e := c.reserveIntegrationClaim(ctx, next)
	if e != nil || claim.State != "HELD" || claim.Generation != failed.Generation+1 {
		t.Fatal("new generation still blocked", e)
	}
	old, e := New(c.db).integrationClaimForApproval(ctx, a)
	if e != nil || old != failed {
		t.Fatal("historical FAILED evidence lost", e)
	}
}
