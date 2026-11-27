package mcphub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// ErrSecretBlocked: the outgoing arguments carry something that looks like
// or is a secret; the call never leaves H96.
var ErrSecretBlocked = errors.New("SECRET_BLOCKED")

// Secret shapes that must never go to a third-party server, whatever the
// caller meant: key blocks, JWTs, well-known token prefixes, credentials in
// URLs and password-like assignments.
var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`\b(gh[pousr]_|github_pat_|glpat-|xox[abprs]-|sk-ant-|sk-proj-|sk-|sk_live_|rk_live_|ya29\.)[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?i)https?://[^/\s:@"]+:[^/\s@"]+@`),
	regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|authorization)\s*[:=]\s*[^\s"]{6,}`),
	regexp.MustCompile(`(?i)"(password|passwd|secret|api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|client[_-]?secret|authorization|private[_-]?key)"\s*:\s*"[^"]{6,}"`),
}

// Guard refuses arguments carrying a known secret (any value stored by the
// hub or added by the host: extra) or a secret shape. Only the reason is
// returned, never the matched text.
func Guard(args any, known []string) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	text := string(raw)
	// JSON escapes; secrets are compared against both forms.
	plain := strings.NewReplacer(`\/`, `/`, `\"`, `"`).Replace(text)
	for _, s := range known {
		if len(s) >= 8 && (strings.Contains(text, s) || strings.Contains(plain, s)) {
			return fmt.Errorf("%w: a stored MCPshka credential is in the arguments", ErrSecretBlocked)
		}
	}
	for _, re := range secretShapes {
		if re.MatchString(plain) {
			return fmt.Errorf("%w: the arguments contain a secret-shaped value (key, token or password)", ErrSecretBlocked)
		}
	}
	return nil
}

// Redact describes arguments for the ledger without their values: each
// top-level key with its JSON type and size. Values never reach the log.
func Redact(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		raw, _ := json.Marshal(args[k])
		kind := "value"
		switch args[k].(type) {
		case string:
			kind = "string"
		case float64, int, int64:
			kind = "number"
		case bool:
			kind = "bool"
		case []any:
			kind = "list"
		case map[string]any:
			kind = "object"
		case nil:
			kind = "null"
		}
		parts = append(parts, fmt.Sprintf("%s:%s(%d)", clip(k, 40), kind, len(raw)))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// scrub removes known secrets and secret shapes from a text bound for the
// ledger or an error message (a server may echo what it received).
func scrub(text string, known []string) string {
	for _, s := range known {
		if len(s) >= 8 {
			text = strings.ReplaceAll(text, s, "[secret]")
		}
	}
	for _, re := range secretShapes {
		text = re.ReplaceAllString(text, "[secret]")
	}
	return text
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// publicOnly refuses connections to loopback, private, link-local and other
// non-public addresses, so a «plugin» URL cannot reach H96 or the LAN
// (Home Assistant, the router) with the hub's credentials.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !Public(ip) {
		return fmt.Errorf("mcphub: %s is not a public address", host)
	}
	return nil
}

// Public reports whether ip is a globally routable unicast address.
func Public(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// CGNAT 100.64/10 (Tailscale, the provider), benchmarking 198.18/15, 0/8.
		if v4[0] == 100 && v4[1]&0xc0 == 64 || v4[0] == 198 && v4[1]&0xfe == 18 || v4[0] == 0 {
			return false
		}
	}
	return true
}
