// Package failure carries diagnostic facts and a suggested call, never a repair.
package failure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Class string

const (
	QuotaExhausted   Class = "QUOTA_EXHAUSTED"
	Disconnected     Class = "DISCONNECTED"
	OwnerBusy        Class = "OWNER_BUSY"
	AuthRequired     Class = "AUTH_REQUIRED"
	PolicyDenied     Class = "POLICY_DENIED"
	ApprovalRequired Class = "APPROVAL_REQUIRED"
	FencedUnknown    Class = "FENCED_UNKNOWN"
	Transient        Class = "TRANSIENT"
	FailedVerified   Class = "FAILED_VERIFIED"
	EvidenceMissing  Class = "HOLD_EVIDENCE_MISSING"
	Deadline         Class = "DEADLINE"
)

var ErrHold = errors.New("executor HOLD: admission or fencing unverified")

type Action struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func (a Action) String() string { b, _ := json.Marshal(a); return string(b) }

type Error struct {
	Class          Class  `json:"class"`
	Disposition    string `json:"disposition"`
	Code           string `json:"reason_code"`
	Site           string `json:"site"`
	Message        string `json:"message"`
	NextSafeAction Action `json:"next_safe_action"`
	Hold           bool   `json:"hold,omitempty"`
	// NoEffect: refused before any side effect (input validation, a
	// precondition checked first). The mutation layer releases the retry
	// key, so the caller may correct the input and reuse it (BUG-20261103-5).
	EffectApplied     bool   `json:"effect_applied,omitempty"`
	TaskID            string `json:"task_id,omitempty"`
	JobID             string `json:"job_id,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	Receipt           any    `json:"receipt,omitempty"`
	NoEffect          bool   `json:"no_effect,omitempty"`
	BudgetMillis      *int64 `json:"budget_ms,omitempty"`
	Deadline          string `json:"deadline,omitempty"`
	cause             error
}

func (e *Error) Error() string {
	budget := ""
	if e.BudgetMillis != nil {
		budget = fmt.Sprintf("; budget_ms=%d deadline=%s", *e.BudgetMillis, e.Deadline)
	}
	message := e.Message
	if e.cause != nil {
		message += ": " + e.cause.Error()
	}
	return fmt.Sprintf("[%s] %s at %s: %s%s; disposition=%s; next_safe_action: %s", e.Class, e.Code, e.Site, message, budget, Disposition(e.Class), e.NextSafeAction.String())
}
func (e *Error) Unwrap() error        { return e.cause }
func (e *Error) Is(target error) bool { return e.Hold && target == ErrHold }

func DefaultAction(class Class) Action {
	a := Action{Tool: "health_snapshot", Arguments: map[string]any{}}
	goal := ""
	switch class {
	case AuthRequired:
		a.Tool = "admin_session_status"
	case QuotaExhausted:
		goal = "quota status and reset time"
	case Disconnected:
		a.Tool = "health_snapshot"
	case FencedUnknown:
		a.Tool = "mutation_contracts"
	// Goals are exact tool names, so tool_resolve answers with that one
	// schema (exact_name) instead of a vague search (0.19.4).
	case Deadline, Transient:
		goal = "idempotency_status" // read the key's receipt, then retry with the same key
	case OwnerBusy:
		goal = "project_lease_status"
	case PolicyDenied:
		goal = "allowed read-only alternative"
	case ApprovalRequired:
		goal = "approval status"
	case FailedVerified:
		goal = "interaction_recovery_diagnose"
	case EvidenceMissing:
		goal = "integration_test_prepare"
	}
	if goal != "" {
		a.Tool = "tool_resolve"
		a.Arguments["goal"] = goal
	}
	return a
}
func NewHold(class Class, code, site, message string, cause error) *Error {
	// Even callers without a context must never turn cancellation into generic HOLD.
	if errors.Is(cause, context.DeadlineExceeded) {
		class = Deadline
		code = "CONTEXT_DEADLINE"
	} else if errors.Is(cause, context.Canceled) {
		class = Transient
		code = "CONTEXT_CANCELED"
	}
	e := &Error{Class: class, Disposition: Disposition(class), Code: code, Site: site, Message: message, NextSafeAction: DefaultAction(class), Hold: true, cause: cause}
	if class == Deadline || errors.Is(cause, context.Canceled) {
		n := int64(0)
		e.BudgetMillis = &n
	}
	return e
}

// Context records the caller's allotted budget (zero means no known budget).
// It retains both the subprocess error and context sentinel for errors.Is.
func Context(ctx context.Context, site string, budget time.Duration, cause error) *Error {
	e := NewHold(Transient, "CONTEXT_CANCELED", site, "context canceled", errors.Join(cause, ctx.Err()))
	if errors.Is(e.cause, context.DeadlineExceeded) {
		e.Class = Deadline
		e.Code = "CONTEXT_DEADLINE"
		e.Message = "context deadline exceeded"
	}
	n := max(int64(0), budget.Milliseconds())
	e.BudgetMillis = &n
	if d, ok := ctx.Deadline(); ok {
		e.Deadline = d.UTC().Format(time.RFC3339Nano)
	}
	e.NextSafeAction = DefaultAction(e.Class)
	return e
}

func Details(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// Rejected is a verified refusal made before any side effect.
func Rejected(code, site, message string, cause error) *Error {
	return RejectedAs(FailedVerified, code, site, message, cause)
}

// RejectedAs is Rejected with a specific class, such as OwnerBusy for a
// caller whose own lease went silent before anything started.
func RejectedAs(class Class, code, site, message string, cause error) *Error {
	e := NewHold(class, code, site, message, cause)
	e.Hold = false
	e.NoEffect = true
	return e
}

// IsNoEffect reports an error that proves nothing was changed.
func IsNoEffect(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.NoEffect
}
