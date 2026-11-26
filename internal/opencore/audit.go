// Package opencore checks a source tree before it is published as the open
// core: personal addresses, tailnet names, private networks, home paths and
// secret-shaped strings must not leave the owner's host. Read-only.
// (Draft by GigaChat-2-Max 24.11, corrected and reviewed.)
package opencore

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Excerpt string `json:"excerpt"`
}

type Report struct {
	Files     int            `json:"files"`
	Findings  []Finding      `json:"findings"`
	ByKind    map[string]int `json:"by_kind"`
	Truncated bool           `json:"truncated"`
}

const maxFile = 1 << 20

var (
	email     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	tailnet   = regexp.MustCompile(`\b[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.ts\.net\b`)
	privateIP = regexp.MustCompile(`\b(?:192\.168|10\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])|100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7]))\.\d{1,3}\.\d{1,3}\b`)
	homePath  = regexp.MustCompile(`/root/|/home/[a-z_][a-z0-9_-]*/`)
	token     = regexp.MustCompile(`\b(?:gh[pousr]_|github_pat_|glpat-|sk-)[A-Za-z0-9_\-]{20,}`)
	pemKey    = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	assigned  = regexp.MustCompile(`(?i)\b(?:token|password|passwd|secret)\s*[:=]\s*["']([^"'\s]{12,})["']`)
	textExt   = map[string]bool{".go": true, ".md": true, ".json": true, ".yml": true, ".yaml": true, ".sh": true, ".py": true, ".txt": true, ".toml": true, ".pem": true, ".key": true, ".env": true, "": true}
	skipDir   = map[string]bool{".git": true, "vendor": true, "node_modules": true, "testdata": true}
)

// Audit walks root in lexical order (filepath.WalkDir) and reports at most
// limit findings (default 500); ByKind counts all of them.
func Audit(root string, limit int) (Report, error) {
	if limit <= 0 {
		limit = 500
	}
	out := Report{ByKind: map[string]int{}, Findings: []Finding{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !textExt[filepath.Ext(d.Name())] {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data[:min(len(data), 8<<10)], 0) >= 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out.Files++
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), maxFile)
		for n := 1; sc.Scan(); n++ {
			for _, f := range scanLine(sc.Text()) {
				out.ByKind[f.Kind]++
				if len(out.Findings) >= limit {
					out.Truncated = true
					continue
				}
				f.File, f.Line = rel, n
				out.Findings = append(out.Findings, f)
			}
		}
		return nil
	})
	return out, err
}

func scanLine(line string) []Finding {
	var out []Finding
	add := func(kind, excerpt string) { out = append(out, Finding{Kind: kind, Excerpt: clip(excerpt)}) }
	for _, m := range email.FindAllString(line, -1) {
		lower := strings.ToLower(m)
		if strings.HasPrefix(lower, "noreply@") || strings.HasSuffix(lower, "@example.com") || strings.HasSuffix(lower, "@example.org") {
			continue
		}
		add("email", line)
	}
	for range tailnet.FindAllString(line, -1) {
		add("tailnet", line)
	}
	for range privateIP.FindAllString(line, -1) {
		add("private_ip", line)
	}
	if homePath.MatchString(line) {
		add("home_path", line)
	}
	for _, m := range token.FindAllString(line, -1) {
		add("secret", mask(m))
	}
	if pemKey.MatchString(line) {
		add("secret", "-----BEGIN …PRIVATE KEY-----")
	}
	for _, m := range assigned.FindAllStringSubmatch(line, -1) {
		add("secret", mask(m[1]))
	}
	return out
}

func mask(s string) string {
	if len(s) <= 4 {
		return "…"
	}
	return s[:4] + "…"
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 120 {
		return s
	}
	cut := 120
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
