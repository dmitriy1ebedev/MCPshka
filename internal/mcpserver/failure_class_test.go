package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/failure"
	"strings"
	"testing"
)

func TestClassifyFailureUsesTodaysRealMessages(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want FailureClass
	}{
		{"fast-forward outcome UNKNOWN; inspect checkout before retry", FailFencedUnknown},
		{"after probe failed; effect UNKNOWN: state", FailFencedUnknown},
		{"codex usage limit reached until 15:32", FailQuotaExhausted},
		{"Face ID administration revoked or superseded: HOLD", FailAuthRequired},
		{"integration is not released by root: HOLD; approval missing", FailApprovalRequired},
		{"interactive holder identity requires reconciliation: HOLD; lease", FailOwnerBusy},
		{"checkout index is locked by another Git operation (12s old): HOLD", FailOwnerBusy},
		{`identity "reader" lacks scope for tool "exec_start"`, FailPolicyDenied},
		{"state operation interrupted: context deadline exceeded", FailTransient},
		{"read unix @->/run/mcpshka-broker/broker.sock: read: connection reset by peer", FailTransient},
		{"dial unix /run/mcpshka-broker/broker.sock: connect: connection refused", FailDisconnected},
		{"executor HOLD: disk reserve: need 9 GB free, have 8 GB", FailFencedUnknown},
		{`unknown tool "x"; find it with tool_resolve`, ""},
		{"lease heartbeat is stale: call project_lease_heartbeat with this lease token, then retry", FailOwnerBusy},
	} {
		err := classifyFailure(errors.New(tc.msg))
		var c *classifiedError
		got := FailureClass("")
		if errors.As(err, &c) {
			got = c.Class
		} else if detail := failure.Details(err); detail != nil {
			got = FailureClass(detail.Class)
		}
		if got != tc.want {
			t.Fatalf("%q: want %q, got %q", tc.msg, tc.want, got)
		}
		if got != "" && (!strings.HasPrefix(err.Error(), "["+string(got)+"] ") || !strings.Contains(err.Error(), "next_safe_action: ")) {
			t.Fatalf("%q: class or action missing from %q", tc.msg, err)
		}
	}
}

func TestClassifyFailureKeepsCauseAndDoesNotRelabel(t *testing.T) {
	base := errors.New("effect UNKNOWN")
	once := classifyFailure(base)
	if !errors.Is(once, base) {
		t.Fatal("errors.Is lost the cause")
	}
	if twice := classifyFailure(once); twice != once {
		t.Fatal("a classified error was wrapped again")
	}
	if classifyFailure(nil) != nil {
		t.Fatal("nil became an error")
	}
}

func TestFailureMarksMatchWholeWords(t *testing.T) {
	for text, want := range map[string]FailureClass{
		"invalid release build preparation":           FailVerified,
		"project lease is held by another writer":     FailOwnerBusy,
		"lease recovery preview-stale-lease refused":  FailOwnerBusy,
		"the release gate is unavailable":             FailDisconnected,
		"approval token required for release_prepare": FailApprovalRequired,
	} {
		var c *classifiedError
		if !errors.As(classifyFailure(errors.New(text)), &c) || c.Class != want {
			t.Fatalf("%q: got %v, want %s", text, c, want)
		}
	}
}

func TestClassifyTypedFailureBeforeText(t *testing.T) {
	cause := errors.New("approval lease quota invalid")
	typed := failure.NewHold(failure.EvidenceMissing, "EVIDENCE_WORKSPACE_MISSING", "release.LocalReleaseReceipt", "evidence missing", cause)
	typed.NextSafeAction = failure.Action{Tool: "integration_test_prepare", Arguments: map[string]any{"evidence": "gitverse", "attempt": 2}}
	got := classifyFailure(typed)
	if got != typed || !errors.Is(got, cause) || !errors.Is(got, failure.ErrHold) {
		t.Fatalf("typed cause changed: %v", got)
	}
	if twice := classifyFailure(got); twice != got {
		t.Fatal("classified twice")
	}
	b, err := json.Marshal(failure.Details(got))
	if err != nil || !strings.Contains(string(b), `"attempt":2`) || !strings.Contains(string(b), `"class":"HOLD_EVIDENCE_MISSING"`) {
		t.Fatalf("wire details: %s %v", b, err)
	}
}
func TestClassifyBareHoldRemainsUnknown(t *testing.T) {
	for _, base := range []error{failure.ErrHold, errors.New(failure.ErrHold.Error()), errors.New("unverified admission: HOLD")} {
		got := classifyFailure(base)
		e := failure.Details(got)
		if e == nil || e.Class != failure.FencedUnknown || e.NextSafeAction.Tool != "mutation_contracts" || !errors.Is(got, base) {
			t.Fatalf("invented verified outcome: %v", got)
		}
		if strings.Contains(got.Error(), "nothing changed") {
			t.Fatal("unproven no-effect claim")
		}
	}
}
func TestClassifyContextSentinels(t *testing.T) {
	for _, tc := range []struct {
		err   error
		class failure.Class
	}{{context.DeadlineExceeded, failure.Deadline}, {context.Canceled, failure.Transient}} {
		got := classifyFailure(tc.err)
		e := failure.Details(got)
		if e == nil || e.Class != tc.class || e.BudgetMillis == nil || !errors.Is(got, tc.err) {
			t.Fatalf("context cause lost: %v", got)
		}
	}
}

func TestStaleOwnLeaseAsksForHeartbeat(t *testing.T) {
	err := classifyFailure(errors.New("lease heartbeat is stale: call project_lease_heartbeat with this lease token, then retry"))
	if !strings.Contains(err.Error(), `"tool":"project_lease_heartbeat"`) {
		t.Fatalf("next action is not a heartbeat: %v", err)
	}
}
