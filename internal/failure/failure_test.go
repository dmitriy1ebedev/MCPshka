package failure

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReasonClassAction(t *testing.T) {
	for _, tc := range []struct {
		class      Class
		tool, args string
	}{
		{QuotaExhausted, "tool_resolve", `{"goal":"quota status and reset time"}`},
		{Disconnected, "health_snapshot", `{}`},
		{OwnerBusy, "tool_resolve", `{"goal":"project_lease_status"}`},
		{AuthRequired, "admin_session_status", `{}`},
		{PolicyDenied, "tool_resolve", `{"goal":"allowed read-only alternative"}`},
		{ApprovalRequired, "tool_resolve", `{"goal":"approval status"}`},
		{FencedUnknown, "mutation_contracts", `{}`},
		{Transient, "tool_resolve", `{"goal":"inspect interrupted operation before retry"}`},
		{FailedVerified, "tool_resolve", `{"goal":"inspect verified failure"}`},
		{EvidenceMissing, "tool_resolve", `{"goal":"integration_test_prepare"}`},
		{Deadline, "tool_resolve", `{"goal":"inspect deadline budget before retry"}`},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			cause := errors.New("underlying cause")
			e := NewHold(tc.class, "TEST_REASON", "test.site", "test failure", cause)
			b, _ := json.Marshal(e.NextSafeAction.Arguments)
			if e.Class != tc.class || e.Code != "TEST_REASON" || e.Site != "test.site" || e.NextSafeAction.Tool != tc.tool || string(b) != tc.args || !errors.Is(e, ErrHold) || !errors.Is(e, cause) || !strings.Contains(e.Error(), cause.Error()) {
				t.Fatalf("contract: %+v args=%s", e, b)
			}
		})
	}
}
func TestContextBudgetAndCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := Context(ctx, "worker.prepare", 720*time.Second, ctx.Err())
	if e.Class != Transient || e.BudgetMillis == nil || *e.BudgetMillis != 720000 || !errors.Is(e, context.Canceled) || !errors.Is(e, ErrHold) {
		t.Fatalf("cancel: %+v", e)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	e = Context(ctx, "worker.prepare", 720*time.Second, ctx.Err())
	if e.Class != Deadline || e.Deadline == "" || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("deadline: %+v", e)
	}
}

func TestDeadlineOutranksJoinedCancellation(t *testing.T) {
	e := NewHold(FencedUnknown, "JOINED", "test.join", "operation interrupted", errors.Join(context.DeadlineExceeded, context.Canceled))
	if e.Class != Deadline || !errors.Is(e, context.DeadlineExceeded) || !errors.Is(e, context.Canceled) {
		t.Fatalf("deadline erased: %v", e)
	}
}
