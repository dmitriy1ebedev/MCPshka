package failure

import (
	"context"
	"errors"
	"strings"
)

// Classified is an error given a class and a next safe action by Classify;
// the original error stays wrapped (errors.Is/As keep working).
type Classified struct {
	Class  Class
	Next   string
	Err    error
	suffix func(string) string
}

func (e *Classified) Error() string {
	tail := ""
	if e.Class == PolicyDenied && e.suffix != nil {
		tail = e.suffix(e.Err.Error())
	}
	return "[" + string(e.Class) + "] " + e.Err.Error() + " — disposition=" + Disposition(e.Class) + "; next_safe_action: " + e.Next + tail
}

func (e *Classified) Unwrap() error { return e.Err }

// failureRules are checked in order: an unknown effect outranks everything,
// because acting on any other class could repeat a mutation.
var rules = []struct {
	class Class
	marks []string
}{
	{FencedUnknown, []string{"effect unknown", "outcome unknown", "outcome is unknown", "remains unknown", "launch unknown", "dispatch unknown", "unknown; reconcile", "effect may"}},
	{PolicyDenied, []string{"native_only", "agent_consent"}},
	{QuotaExhausted, []string{"usage limit", "session limit", "quota"}},
	{AuthRequired, []string{"face id", "webauthn", "administrative session", "admin session", "passkey"}},
	{ApprovalRequired, []string{"approval"}},
	{OwnerBusy, []string{"lease", "already active", "locked by", "busy", "in progress elsewhere", "index is locked"}},
	{PolicyDenied, []string{"lacks scope", "cannot call mutating", "allowlist", "policy", "not permitted", "denied"}},
	{Transient, []string{"deadline exceeded", "timed out", "timeout", "connection reset", "temporarily", "interrupted", "try again"}},
	{Disconnected, []string{"connection refused", "broken pipe", "no such file or directory: /run", "unavailable"}},
	{FailedVerified, []string{"invalid", "required", "must", "rejected", "mismatch"}},
}

// Classify gives an unclassified error a class and one deterministic next
// safe action (ROADMAP S1). denialSuffix, when set, adds the route of a
// policy denial to the text.
func Classify(err error, denialSuffix func(string) string) error {
	if err == nil {
		return nil
	}
	var done *Classified
	if errors.As(err, &done) {
		return err
	}

	if Details(err) != nil {
		return err
	}
	if errors.Is(err, ErrHold) || err.Error() == ErrHold.Error() {
		return NewHold(FencedUnknown, "HOLD_REASON_MISSING", "mcpserver.classifyFailure", "legacy HOLD without diagnostic evidence; inspect operation before retry", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return Context(context.Background(), "mcpserver.classifyFailure", 0, err)
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "lease receipt") || strings.Contains(text, "resume receipt is stale") {
		e := NewHold(EvidenceMissing, "STALE_LEASE_RECEIPT", "lease.reconcile", "cached ownership is no longer current; inspect holder before acquiring again", err)
		e.NextSafeAction = Action{Tool: "tool_resolve", Arguments: map[string]any{"goal": "project_context"}}
		return e
	}
	// The caller's own lease went silent: its next step is a heartbeat with
	// the token it holds, not waiting for "another owner".
	if strings.Contains(text, "lease heartbeat is stale") {
		return &Classified{Class: OwnerBusy, Next: Action{Tool: "project_lease_heartbeat", Arguments: map[string]any{}}.String(), Err: err, suffix: denialSuffix}
	}
	for _, rule := range rules {
		for _, mark := range rule.marks {
			if ContainsMark(text, mark) {
				return &Classified{Class: rule.class, Next: DefaultAction(rule.class).String(), Err: err, suffix: denialSuffix}
			}
		}
	}

	if ContainsMark(text, "hold") || ContainsMark(text, "unverified") {
		return NewHold(FencedUnknown, "HOLD_REASON_UNCLASSIFIED", "mcpserver.classifyFailure", "HOLD evidence has no typed reason; inspect operation before retry", err)
	}
	return err
}

// ContainsMark finds mark as a whole word or phrase: "release" must not read
// as "lease" (an invalid release request was classified OWNER_BUSY,
// BUG-20261023-64).
func ContainsMark(text, mark string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], mark)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(mark)
		if (i == 0 || !wordByte(text[i-1])) && (end == len(text) || !wordByte(text[end])) {
			return true
		}
		from = i + 1
	}
}

func wordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}
