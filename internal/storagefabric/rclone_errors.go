package storagefabric

import (
	"errors"
	"regexp"
	"strings"
)

// commandError keeps why rclone failed: a swallowed stderr turned every
// failed read into "UNKNOWN, reconcile" with nothing to act on.
type commandError struct{ reason string }

func (e *commandError) Error() string { return "storage command failed: " + e.reason }

var (
	secretLike = regexp.MustCompile(`[A-Za-z0-9_\-./+=]{40,}`)
	authFail   = regexp.MustCompile(`(?i)invalid_grant|token (has been )?(expired|revoked)|couldn't fetch token|failed to (get|refresh) token|unauthori[sz]ed|\b401\b|invalid_client|access_denied`)
	notFound   = regexp.MustCompile(`(?i)directory not found|object not found|file not found|not found`)
	quotaFail  = regexp.MustCompile(`(?i)quota|rate ?limit|\b429\b|storageQuotaExceeded`)
	netFail    = regexp.MustCompile(`(?i)no such host|i/o timeout|connection (refused|reset)|TLS handshake|network is unreachable|context deadline`)
)

// rcloneReason names the failure class plus the last error line with anything
// token-like masked; credentials never appear in rclone's ERROR lines, the
// mask is a second fence.
func rcloneReason(stderr string) string {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
		}
	}
	// rclone prints "…: , rateLimitExceeded" for an empty API message.
	line = strings.TrimLeft(strings.ReplaceAll(secretLike.ReplaceAllString(line, "***"), ": , ", ": "), " ,:")
	if len(line) > 240 {
		line = line[:240]
	}
	class := "ERROR"
	switch {
	case authFail.MatchString(stderr):
		class = "AUTH_REQUIRED (reconnect: storage_connect provider=<cloud>)"
	case quotaFail.MatchString(stderr):
		class = "QUOTA_OR_RATE_LIMIT"
	case netFail.MatchString(stderr):
		class = "NETWORK"
	case notFound.MatchString(stderr):
		class = "NOT_FOUND"
	}
	if line == "" {
		return class
	}
	return class + ": " + line
}

func failureReason(err error) string {
	var ce *commandError
	if errors.As(err, &ce) {
		return ce.reason
	}
	return err.Error()
}

// rcloneFailure is ErrUnknown for effects (errors.Is) and carries the reason.
type rcloneFailure struct{ reason string }

func (f *rcloneFailure) Error() string { return ErrUnknown.Error() + ": " + f.reason }
func (f *rcloneFailure) Unwrap() error { return ErrUnknown }

func reasonOf(err error) string {
	var f *rcloneFailure
	if errors.As(err, &f) {
		return f.reason
	}
	return failureReason(err)
}

// definiteRefusal: the provider refused before any effect (auth or quota/rate
// limit), so the operation is FAILED, not UNKNOWN; UNKNOWN kept the storage
// worker fenced after a plain rateLimitExceeded (MCP-165).
func definiteRefusal(reason string) bool {
	return strings.HasPrefix(reason, "AUTH_REQUIRED") || strings.HasPrefix(reason, "QUOTA_OR_RATE_LIMIT")
}

// exitReason adds what only rclone's exit code says: at --log-level=ERROR a
// missing path prints nothing and exits 3 (directory) or 4 (file), so the
// text alone read as an unclassified failure and a read-back could never
// tell «not there» from «could not look».
func exitReason(bin string, code int, reason string) string {
	if bin == Rclone && (code == 3 || code == 4) && strings.HasPrefix(reason, "ERROR") {
		return "NOT_FOUND" + strings.TrimPrefix(reason, "ERROR")
	}
	return reason
}
