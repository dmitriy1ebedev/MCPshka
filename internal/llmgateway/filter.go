package llmgateway

import (
	"math"
	"regexp"
	"strings"
)

// Egress filter (owner decision 17.11, m-7b7f4331326c / m-7184e949d2d2):
// code and test logs may go to any provider; secrets never. A secret-like
// match is removed for LLM sends; Filter keeps strict refusal for other callers.

var denyPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)},
	{"bearer", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
	{"authorization", regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*[^\r\n]+`)},
	{"cookie", regexp.MustCompile(`(?i)\b(set-)?cookie\s*:\s*[^\r\n]+`)},
	{"assignment", regexp.MustCompile(`(?i)\b[a-z0-9_.-]*(password|passwd|secret|token|api[_-]?key|psk|private[_-]?key|client[_-]?secret)[a-z0-9_.-]*["']?\s*[:=]{1,2}\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\r\n,;}]+)`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"provider_key", regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[abp]-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|tskey-[A-Za-z0-9-]{20,})`)},
	{"wireguard_key", regexp.MustCompile(`(?i)\b(privatekey|presharedkey)\s*=\s*[A-Za-z0-9+/]{42,44}=`)},
	{"secret_path", regexp.MustCompile(`(/etc/(shadow|mcpshka/|diag/ha\.token|lane/)|/var/lib/mcpshka/secrets/|\.env\b|/root/\.ssh/)[^\s"'\x60<>]*`)},
}

var (
	privIPv4 = regexp.MustCompile(`\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3})\b`)
	email    = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)
	macAddr  = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
	word     = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
	hexOnly  = regexp.MustCompile(`^[0-9a-f]{32,64}$`)
)

// Verdict is the filter's decision for one outgoing text.
type Verdict struct {
	Allowed    bool           `json:"allowed"`
	Reasons    []string       `json:"reasons,omitempty"`
	Redactions int            `json:"redactions"`
	Text       string         `json:"-"`
	Types      map[string]int `json:"redaction_types,omitempty"`
}

// Filter decides whether text may leave the host and returns the redacted text.
func Filter(text string) Verdict {
	v := Redact(text)
	for _, reason := range v.Reasons {
		if reason != "lan_ip" && reason != "mac" && reason != "email" {
			v.Allowed = false
		}
	}
	return v
}

// Redact removes secret-like spans while keeping the rest of the request usable.
func Redact(text string) Verdict {
	v := Verdict{Allowed: true, Types: map[string]int{}}
	replace := func(kind string) string {
		v.Redactions++
		if v.Types[kind] == 0 {
			v.Reasons = append(v.Reasons, kind)
		}
		v.Types[kind]++
		return "[REDACTED:" + kind + "]"
	}
	for _, p := range denyPatterns {
		text = p.re.ReplaceAllStringFunc(text, func(string) string { return replace(p.name) })
	}
	text = word.ReplaceAllStringFunc(text, func(w string) string {
		if !hexOnly.MatchString(w) && strings.Trim(w, "-_=/") != "" && entropy(w) >= 4.3 {
			return replace("high_entropy")
		}
		return w
	})
	text = privIPv4.ReplaceAllStringFunc(text, func(string) string { return replace("lan_ip") })
	text = macAddr.ReplaceAllStringFunc(text, func(string) string { return replace("mac") })
	text = email.ReplaceAllStringFunc(text, func(string) string { return replace("email") })
	v.Text = text
	return v
}

func entropy(s string) float64 {
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	h := 0.0
	for _, c := range freq {
		if c > 0 {
			p := float64(c) / float64(len(s))
			h -= p * math.Log2(p)
		}
	}
	return h
}
