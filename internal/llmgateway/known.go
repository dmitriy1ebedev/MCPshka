package llmgateway

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Known secrets (owner 17.11: «особое внимание токенам и личному, что
// подключено к MCPshka»): patterns miss tokens without a recognisable shape,
// so every value MCPshka itself holds — secret files, env files, JSON
// credentials — is matched literally. A text that contains any of them, or
// its first 12 characters, is redacted before anything leaves the host.
var SecretSources = []string{"/var/lib/mcpshka/secrets", "/var/lib/mcpshka/github", "/var/lib/mcpshka/forges", "/etc/mcpshka", "/etc/diag/ha.token", "/var/lib/lane/secrets"}

const (
	minSecret     = 8
	secretPrefix  = 12
	maxSecretFile = 256 << 10
	secretTTL     = 10 * time.Minute
)

var (
	envLine  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*[=:]\s*["']?([^"'\s#]+)`)
	secretKV = regexp.MustCompile(`(?i)(key|token|secret|pass|psk|auth|cookie|private|credential)`)
)

type knownSet struct {
	mu     sync.Mutex
	at     time.Time
	values []string
}

var known knownSet

// knownSecrets returns the literal values to redact, reloaded every 10 min.
// Unreadable files are skipped: the service sees what it can use, and what
// it cannot read it cannot leak either.
func knownSecrets() []string {
	known.mu.Lock()
	defer known.mu.Unlock()
	if time.Since(known.at) < secretTTL && known.at.After(time.Time{}) {
		return known.values
	}
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if len(v) >= minSecret && !seen[v] {
			seen[v] = true
		}
	}
	for _, root := range SecretSources {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, ".md") {
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > maxSecretFile {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			collect(string(raw), add)
			return nil
		})
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && secretKV.MatchString(k) {
			add(v)
		}
	}
	vals := make([]string, 0, len(seen))
	for v := range seen {
		vals = append(vals, v)
	}
	known.values, known.at = vals, time.Now()
	return vals
}

// collect takes a whole one-line file as a value, JSON string fields whose
// key looks secret, and env-style assignments.
func collect(text string, add func(string)) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if !strings.ContainsAny(t, "\n=:{") {
		add(t)
		return
	}
	var v any
	if json.Unmarshal([]byte(t), &v) == nil {
		walkJSON("", v, add)
		return
	}
	for _, m := range envLine.FindAllStringSubmatch(t, -1) {
		if secretField(m[1], m[2]) {
			add(m[2])
		}
	}
	if !strings.Contains(t, "\n") {
		add(t)
	}
}

func walkJSON(key string, v any, add func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			walkJSON(k, c, add)
		}
	case []any:
		for _, c := range x {
			walkJSON(key, c, add)
		}
	case string:
		if secretField(key, x) {
			add(x)
		}
	}
}

// leaksKnown reports whether text holds a known secret or its prefix.
func leaksKnown(text string, secrets []string) bool {
	for _, s := range secrets {
		if strings.Contains(text, s) || (len(s) > secretPrefix && strings.Contains(text, s[:secretPrefix])) {
			return true
		}
	}
	return false
}

// secretField excludes credential metadata, never credential values.
func secretField(key, value string) bool {
	k := strings.ToLower(key)
	for _, suffix := range []string{"_file", "_path"} {
		if strings.HasSuffix(k, suffix) && (strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../")) {
			return false
		}
	}
	return secretKV.MatchString(key)
}

// redactKnown removes full values first; a prefix match also consumes the
// remaining token so truncation cannot leak a credential suffix.
func redactKnown(text string, secrets []string) (string, int) {
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	n := 0
	for _, s := range secrets {
		if s == "" {
			continue
		}

		needle := s[:min(len(s), secretPrefix)]
		var out strings.Builder
		for {
			i := strings.Index(text, needle)
			if i < 0 {
				out.WriteString(text)
				break
			}
			end := i + len(needle)
			if strings.HasPrefix(text[i:], s) {
				end = i + len(s)
			}
			for end < len(text) && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~+/=", rune(text[end])) {
				end++
			}
			out.WriteString(text[:i])
			out.WriteString("[REDACTED:known_secret]")
			text = text[end:]
			n++
		}
		text = out.String()

	}
	return text, n
}

func (g *Gateway) redact(text string, extra ...string) Verdict {
	text, n := redactKnown(text, append(append([]string(nil), g.secrets()...), extra...))
	v := Redact(text)
	if n > 0 {
		v.Redactions += n
		v.Types["known_secret"] += n
		v.Reasons = append(v.Reasons, "known_secret")
	}
	return v
}
