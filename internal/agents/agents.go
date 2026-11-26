// Package agents runs a coding agent non-interactively for MCPshka: Codex CLI
// or Claude Code. Both reach the same MCPshka tools, so either can take a
// task the other started; when one hits its usage limit the caller can switch
// to the other or wait for the reported reset.
package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/budget"
)

const (
	Codex  = "codex"
	Claude = "claude"
)

// Known lists the supported agents in default preference order.
var Known = []string{Codex, Claude}

// QuotaError reports an agent usage limit and, when the agent said so, when
// it resets.
type QuotaError struct {
	Agent string
	Reset time.Time // zero when unknown
}

func (e *QuotaError) Error() string {
	if e.Reset.IsZero() {
		return e.Agent + " usage limit reached"
	}
	return e.Agent + " usage limit reached until " + e.Reset.Format("15:04")
}

// IsQuota reports whether err is an agent usage limit.
func IsQuota(err error) (*QuotaError, bool) {
	var q *QuotaError
	ok := errors.As(err, &q)
	return q, ok
}

var (
	codexReset = regexp.MustCompile(`try again at (\d{1,2}):(\d{2})\s*([AP]M)`)
	// Multi-day limits print a date: "try again at Nov 6th, 2026 10:20 AM"
	// (MCP-007: the dated form was reported as an unknown reset).
	codexDated  = regexp.MustCompile(`try again at ([A-Z][a-z]{2})[a-z]*\.? (\d{1,2})(?:st|nd|rd|th)?,? (\d{4}),? (\d{1,2}):(\d{2})\s*([AP]M)`)
	claudeReset = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)`)
	quotaText   = regexp.MustCompile(`(?i)usage limit|session limit|rate limit reached|limit reached`)
)

// parseReset turns "10:17 PM" style times into the next such moment.
func parseReset(text string, now time.Time) time.Time {
	if d := codexDated.FindStringSubmatch(text); d != nil {
		if month, err := time.Parse("Jan", d[1]); err == nil {
			h := atoi(d[4]) % 12
			if d[6] == "PM" {
				h += 12
			}
			t := time.Date(atoi(d[3]), month.Month(), atoi(d[2]), h, atoi(d[5]), 0, 0, now.Location())
			if t.After(now) {
				return t
			}
		}
		return time.Time{}
	}
	m := codexReset.FindStringSubmatch(text)
	if m == nil {
		if c := claudeReset.FindStringSubmatch(text); c != nil {
			m = []string{c[0], c[1], c[2], strings.ToUpper(c[3])}
		}
	}
	if m == nil {
		return time.Time{}
	}
	h, min := atoi(m[1]), atoi(m[2])
	if m[3] == "PM" && h != 12 {
		h += 12
	}
	if m[3] == "AM" && h == 12 {
		h = 0
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}

// Command builds the non-interactive invocation for an agent. Codex runs with
// full access in dir (as the existing operator sessions do); Claude Code runs
// in auto permission mode with MCPshka tools allowed.
func Command(ctx context.Context, agent, dir, prompt, report string) (*exec.Cmd, error) {
	switch agent {
	case Codex:
		return exec.CommandContext(ctx, "codex", "exec", "-s", "danger-full-access", "-c", "model_reasoning_effort=medium", "-C", dir, "-o", report, prompt), nil
	case Claude:
		cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--permission-mode", "auto", "--allowedTools", "mcp__mcpshka", "--output-format", "text")
		cmd.Dir = dir
		return cmd, nil
	}
	a, err := loadAdapter(agent)
	if err != nil {
		return nil, err
	}
	return a.command(ctx, dir, prompt, report), nil
}

// Run executes one agent session and returns its output tail. A usage limit
// comes back as *QuotaError with the reset time when the agent printed one.
func Run(ctx context.Context, agent, dir, prompt, report string) (string, error) {
	cmd, err := Command(ctx, agent, dir, prompt, report)
	if err != nil {
		return "", err
	}
	return runCommand(ctx, cmd, agent, report)
}

func runCommand(ctx context.Context, cmd *exec.Cmd, agent, report string) (string, error) {
	out := budget.Output{Limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	tail := redactTaskSecret(ctx, out.String())
	transport := transportFinal(tail)
	if agent == Claude && report != "" {
		if err := writeReport(report, []byte(tail)); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if agent == Codex {
		if last, err := readReportTail(report); err == nil {
			out.Write([]byte("\n"))
			out.Write(last)
			tail = redactTaskSecret(ctx, out.String())
			if secret, _ := ctx.Value(taskSecretKey{}).(string); secret != "" {
				if err := writeReport(report, []byte(redactTaskSecret(ctx, string(last)))); err != nil {
					runErr = errors.Join(runErr, err)
				}
			}
		}
	}
	if ctx.Err() != nil {
		return tail, ctx.Err()
	}
	// Claude Code says "session limit", Codex "usage limit" (BUG-20261022-46).
	// A clean exit is a limit only if the agent's last words say so: a
	// finished Codex task that had read tool descriptions mentioning limits
	// was marked quota (BUG-20261023-83).
	if quotaText.MatchString(tail) && (runErr != nil || quotaFinal(tail)) {
		return tail, &QuotaError{Agent: agent, Reset: parseReset(tail, time.Now())}
	}
	// "Selected model is at capacity" is the provider's transient overload, not
	// an account limit; reported as a bare "exit status 1" it read as a broken
	// Codex integration (BUG-20261026-130).
	if runErr != nil && capacityFinal(tail) {
		return tail, &CapacityError{Agent: agent}
	}
	// When a Face ID work-session expires mid-run, the CLI may already have
	// edited files or applied effects. A failed CLI exit does not prove that
	// nothing happened. Do not classify this as an ordinary retryable failure.
	// Preserve a durable UNKNOWN receipt and require task/lease/unit reconciliation.
	if runErr != nil && expiredOwnerSessionFinal(tail) {
		return tail, fmt.Errorf("owner authorization expired during task; reconcile before continuation: %w", errAttemptUnknown)
	}
	if runErr != nil && transport {
		return tail, &TransportError{Agent: agent, Cause: runErr}
	}
	return tail, runErr
}

// expiredOwnerSessionFinal uses explicit MCPshka/Face ID diagnostics in the
// last lines only: prose that happens to contain "expired" is not proof.
func expiredOwnerSessionFinal(tail string) bool {
	if len(tail) > 1024 {
		tail = tail[len(tail)-1024:]
	}
	tail = strings.ToLower(tail)
	for _, evidence := range []string{
		"admin session expired",
		"administrative session expired",
		"admin session is not active",
		"admin_session_id expired",
		"face id session expired",
		"face id authorization expired",
		"mcpshka: auth_required",
		"mcpshka: owner authorization expired",
	} {
		if strings.Contains(tail, evidence) {
			return true
		}
	}
	return false
}

// CapacityError classifies provider overload; it cannot prove earlier effects.
type CapacityError struct{ Agent string }

func (e *CapacityError) Error() string {
	return e.Agent + " model at capacity (provider-side, transient): reconcile the original task, worktree and receipts before continuing"
}

// capacityFinal reports a model-capacity refusal in the last lines of a
// failed run.
func capacityFinal(tail string) bool {
	if len(tail) > 512 {
		tail = tail[len(tail)-512:]
	}
	return strings.Contains(strings.ToLower(tail), "model is at capacity")
}

// quotaFinal reports a usage or session limit in the last lines of a clean
// exit, where an agent that stopped on its limit prints it.
func quotaFinal(tail string) bool {
	if len(tail) > 512 {
		tail = tail[len(tail)-512:]
	}
	tail = strings.ToLower(tail)
	return strings.Contains(tail, "usage limit") || strings.Contains(tail, "session limit")
}

// TransportError is a failed executor connection, after its attempt has ended.
type TransportError struct {
	Agent string
	Cause error
}

func (e *TransportError) Error() string {
	return e.Agent + " transport unavailable: " + e.Cause.Error()
}
func (e *TransportError) Unwrap() error { return e.Cause }

func transportFinal(tail string) bool {
	if len(tail) > 2048 {
		tail = tail[len(tail)-2048:]
	}
	tail = strings.ToLower(tail)
	for _, diagnostic := range []string{
		"failed to refresh available models: request timed out",
		"workspace routing discovery timed out",
		"error: request timed out",
		"error: dns error",
		"error: network is unreachable",
		"stream disconnected before completion",
		"error sending request for url",
		"failed to lookup address information",
	} {
		if strings.Contains(tail, diagnostic) {
			return true
		}
	}
	if strings.Contains(tail, "chatgpt.com") || strings.Contains(tail, "api.openai.com") || strings.Contains(tail, "api.anthropic.com") {
		for _, diagnostic := range []string{"timed out", "timeout", "dns error", "connection refused", "connection reset", "network is unreachable", "temporary failure in name resolution", "reconnecting", "service unavailable"} {
			if strings.Contains(tail, diagnostic) {
				return true
			}
		}
	}
	return false
}
