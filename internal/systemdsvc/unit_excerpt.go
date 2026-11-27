package systemdsvc

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

var workflowUnit = regexp.MustCompile(`^mcpshka-(integration-admin-[a-f0-9]{32}|roadmap-[A-Za-z0-9_-]{1,128})$`)
var logBearer = regexp.MustCompile(`(?i)(bearer\s+)[^\s,"']+`)
var logCredential = regexp.MustCompile(`(?i)(bearer\s+|(?:[a-z_]*(?:token|secret|password|passwd|api_key|authorization)[a-z_]*["']?\s*[:=]\s*["']?))[^\s,"']+`)
var logURLCredential = regexp.MustCompile(`(https?://)[^\s/@]+:[^\s/@]+@`)

// WorkflowExcerpt reads only a caller-derived workflow unit, never a unit
// named by agent output. The excerpt is untrusted diagnostic evidence.
func WorkflowExcerpt(ctx context.Context, unit string) (string, error) {
	if !workflowUnit.MatchString(unit) {
		return "", errors.New("invalid workflow unit")
	}
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, _, err := runJournalctl(child, 8192, "--no-pager", "--output=cat", "--lines=30", "--unit="+unit+".service")
	if err != nil {
		return "", err
	}
	return DiagnosticText(string(raw)), nil
}

// RedactLine masks bearer tokens, key=value secrets and URL credentials in
// one journal line (service_logs output for DIAG ACTIVE-08 / MCP-123).
func RedactLine(raw string) string {
	raw = logBearer.ReplaceAllString(raw, "${1}[REDACTED]")
	raw = logCredential.ReplaceAllString(raw, "${1}[REDACTED]")
	return logURLCredential.ReplaceAllString(raw, "${1}[REDACTED]@")
}

func DiagnosticText(raw string) string {
	raw = RedactLine(raw)
	raw = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, raw)
	if len(raw) > 4096 {
		raw = raw[len(raw)-4096:]
	}
	return strings.TrimSpace(raw)
}
