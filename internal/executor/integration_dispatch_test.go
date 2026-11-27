package executor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestIntegrationDispatchPersistsBeforeExternalStartAndNeverReplays(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	m, err := c.prepareIntegrationLaunch(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	before, err := c.integrationRuntime(ctx, a)
	if err != nil || before.Dispatched {
		t.Fatal(err)
	}
	if err := c.recordIntegrationDispatch(ctx, m); err != nil {
		t.Fatal(err)
	}
	after, err := New(c.db).integrationRuntime(ctx, a)
	if err != nil || !after.Dispatched || after.Worker.Launches[0].Identity != nil {
		t.Fatal("scheduling became fabricated running identity", err)
	}
	if err := New(c.db).recordIntegrationDispatch(ctx, m); !errors.Is(err, ErrHold) {
		t.Fatal("unknown scheduling attempt replayed", err)
	}
}
func TestIntegrationDispatchRejectsClosedOrChangedGeneration(t *testing.T) {
	for _, kind := range []string{"closed", "generation", "binding", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			c, a := integrationLaunchFixture(t)
			ctx := context.Background()
			m, err := c.prepareIntegrationLaunch(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "closed":
				if err := c.Close(ctx, m.Pin); err != nil {
					t.Fatal(err)
				}
			case "generation":
				claim := m.Claim
				claim.Generation++
				raw, _ := json.Marshal(claim)
				if err := c.db.Put(integrationClaimKey(claim.Repository.CommonDir), raw); err != nil {
					t.Fatal(err)
				}
			case "binding":
				if err := c.db.Put(integrationWorkerKey(m.Claim.ExecutionID), []byte("{}")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := c.recordIntegrationDispatch(ctx, m); err == nil {
				t.Fatal("invalid launch authority accepted")
			}
			if _, err := c.db.Get(integrationDispatchKey(m.LaunchID)); err == nil {
				t.Fatal("invalid attempt persisted")
			}
		})
	}
}
func TestIntegrationRuntimeRejectsCorruptDispatchRecord(t *testing.T) {
	c, a := integrationLaunchFixture(t)
	ctx := context.Background()
	m, err := c.prepareIntegrationLaunch(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.db.Put(integrationDispatchKey(m.LaunchID), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.integrationRuntime(ctx, a); !errors.Is(err, ErrHold) {
		t.Fatal("corrupt receipt accepted", err)
	}
}
