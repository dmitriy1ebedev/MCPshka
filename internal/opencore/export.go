package opencore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ConfigPath is the export policy inside the exported commit itself, so the
// same commit always yields the same tree.
const ConfigPath = ".mcpshka/opencore.json"

// Config says what of the canonical tree stays private. Exclude entries are
// path.Match globs tried against every path prefix ("docs/history" drops
// the folder, "docs/ISSUE_REGISTRY_*.md" the files).
type Config struct {
	Repository string `json:"repository"`
	// Targets are the public repositories (default: GitHub Repository).
	Targets []Target `json:"targets,omitempty"`
	// Overlay is a directory whose files replace or add paths in the export
	// only (the public CI); the directory itself is not exported.
	Overlay string   `json:"overlay,omitempty"`
	Exclude []string `json:"exclude"`
	// Fixtures are globs of files whose secret-shaped strings are known fakes
	// (the secret scanners' own tests); every other secret finding blocks.
	Fixtures []string `json:"fixtures"`
}

func match(globs []string, rel string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, rel); ok {
			return true
		}
	}
	return false
}

// Rule replaces one owner identifier (address, tailnet name, MAC) with a
// neutral placeholder of the same shape, so tests keep their meaning.
type Rule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Manifest describes one export; Digest covers every path, mode and content.
type Manifest struct {
	Source     string    `json:"source_commit"`
	Repository string    `json:"repository"`
	Targets    []Target  `json:"targets"`
	Files      int       `json:"files"`
	Excluded   int       `json:"excluded"`
	Replaced   int       `json:"replaced_files"`
	Digest     string    `json:"tree_digest"`
	Blocking   []Finding `json:"blocking,omitempty"`
	Audit      Report    `json:"audit"`
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (c Config) excluded(rel string) bool {
	if rel == ConfigPath {
		return true
	}
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		for _, g := range c.Exclude {
			if ok, _ := path.Match(strings.TrimSuffix(g, "/"), prefix); ok {
				return true
			}
		}
	}
	return false
}

// Export writes the open core of commit into dest (which must not exist):
// git archive of the exact commit, minus the excluded paths, with rules
// applied to text files. It refuses to leave a tree that still holds a
// secret-shaped string or any rule's From (ErrBlocked, dest removed).
func Export(ctx context.Context, repo, commit, dest string, rules []Rule) (Manifest, error) {
	if !shaRE.MatchString(commit) {
		return Manifest{}, errors.New("commit must be a full 40-hex SHA")
	}
	if _, err := os.Lstat(dest); err == nil {
		return Manifest{}, errors.New("destination already exists")
	}
	raw, err := gitOut(ctx, repo, "show", commit+":"+ConfigPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s missing in %s: the commit has no export policy", ConfigPath, commit[:12])
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ConfigPath, err)
	}
	rules = normalize(rules)
	version := ""
	if v, err := gitOut(ctx, repo, "show", commit+":VERSION"); err == nil {
		version = strings.TrimSpace(string(v))
	}
	archive, err := gitOut(ctx, repo, "archive", "--format=tar", commit)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return Manifest{}, err
	}
	m := Manifest{Source: commit, Repository: cfg.Repository, Targets: cfg.Targets}
	if len(m.Targets) == 0 {
		m.Targets = []Target{GitHubTarget(cfg.Repository)}
	}
	type entry struct {
		rel, sum string
		mode     int64
	}
	var entries []entry
	// Overlay paths win over the same path of the tree, whatever the order.
	overlay := map[string]bool{}
	prefix := strings.TrimSuffix(cfg.Overlay, "/") + "/"
	if cfg.Overlay != "" {
		scan := tar.NewReader(bytes.NewReader(archive))
		for h, err := scan.Next(); err == nil; h, err = scan.Next() {
			if rel, ok := strings.CutPrefix(path.Clean(h.Name), prefix); ok && h.Typeflag == tar.TypeReg {
				overlay[rel] = true
			}
		}
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(dest)
			return Manifest{}, err
		}
		rel := path.Clean(h.Name)
		if h.Typeflag != tar.TypeReg || rel == "." || strings.HasPrefix(rel, "../") || rel == "pax_global_header" {
			continue
		}
		if o, ok := strings.CutPrefix(rel, prefix); ok && cfg.Overlay != "" {
			rel = o
		} else if overlay[rel] {
			continue
		}
		if cfg.excluded(rel) {
			m.Excluded++
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			os.RemoveAll(dest)
			return Manifest{}, err
		}
		if out, changed := apply(data, rules); changed {
			data = out
			m.Replaced++
		}
		if rel == "README.md" || rel == "README.ru.md" {
			data = VersionBadge(data, version)
		}
		mode := int64(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			os.RemoveAll(dest)
			return Manifest{}, err
		}
		if err := os.WriteFile(target, data, os.FileMode(mode)); err != nil {
			os.RemoveAll(dest)
			return Manifest{}, err
		}
		sum := sha256.Sum256(data)
		entries = append(entries, entry{rel, hex.EncodeToString(sum[:]), mode})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	d := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(d, "%o %s %s\n", e.mode, e.sum, e.rel)
	}
	m.Files, m.Digest = len(entries), hex.EncodeToString(d.Sum(nil))
	if m.Audit, m.Blocking, err = auditExport(dest, rules, cfg.Fixtures); err != nil {
		os.RemoveAll(dest)
		return m, err
	}
	return m, nil
}

func auditExport(dest string, rules []Rule, fixtures []string) (Report, []Finding, error) {
	rep, err := Audit(dest, 5000)
	if err != nil {
		return rep, nil, err
	}
	findings, err := blocking(dest, rep, rules, fixtures)
	if err == nil && len(findings) != 0 {
		err = ErrBlocked
	}
	return rep, findings, err
}

// ErrBlocked: the export still carries a secret or an owner identifier.
var ErrBlocked = errors.New("open core export blocked: secret-shaped strings or owner identifiers remain (see blocking)")

// Secret kinds always block; the generic kinds (email, private IP, tailnet,
// home path) are mostly fixtures and product defaults and only count.
func blocking(dest string, rep Report, rules []Rule, fixtures []string) ([]Finding, error) {
	var out []Finding
	seen := 0
	for _, f := range rep.Findings {
		if f.Kind != "secret" {
			continue
		}
		seen++
		if !match(fixtures, f.File) {
			out = append(out, f)
		}
	}
	if rep.ByKind["secret"] > seen {
		out = append(out, Finding{Kind: "secret", Excerpt: "secret findings beyond the audit limit"})
	}
	if len(rules) == 0 {
		return out, nil
	}
	err := filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, r := range rules {
			if _, left := apply(data, []Rule{r}); left {
				rel, _ := filepath.Rel(dest, p)
				out = append(out, Finding{File: rel, Kind: "owner_identifier", Excerpt: mask(r.From)})
			}
		}
		return nil
	})
	return out, err
}

// normalize drops empty and self rules and orders longer identifiers first,
// so "a.tail1.ts.net" is replaced before "tail1.ts.net".
func normalize(rules []Rule) []Rule {
	seen := map[string]bool{}
	var out []Rule
	for _, r := range rules {
		r.From, r.To = strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		if len(r.From) < 4 || r.From == r.To || seen[strings.ToLower(r.From)] {
			continue
		}
		seen[strings.ToLower(r.From)] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].From) > len(out[j].From) })
	return out
}

// apply replaces whole identifiers only: "192.168.0.17" must not rewrite
// "192.168.77.10" or "10.192.168.0.17", so a match touching another letter
// or digit on a word-edged side is left alone. Case-insensitive (MACs, DNS).
func apply(data []byte, rules []Rule) ([]byte, bool) {
	if len(rules) == 0 || bytes.IndexByte(data[:min(len(data), 8<<10)], 0) >= 0 {
		return data, false
	}
	changed := false
	for _, r := range rules {
		from := []byte(strings.ToLower(r.From))
		if !bytes.Contains(bytes.ToLower(data), from) {
			continue
		}
		low := bytes.ToLower(data)
		var out bytes.Buffer
		last := 0
		for i := 0; ; {
			j := bytes.Index(low[i:], from)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(from)
			// A dotted number before an address makes it part of a longer
			// one; a dot before a DNS suffix is just the next label.
			whole := (start == 0 || !isWord(from[0]) || !isWord(low[start-1]) && !(isDigit(from[0]) && low[start-1] == '.')) &&
				(end == len(low) || !isWord(from[len(from)-1]) || !isWord(low[end]))
			if whole {
				out.Write(data[last:start])
				out.WriteString(r.To)
				last, changed = end, true
			}
			i = end
		}
		if last > 0 {
			out.Write(data[last:])
			data = out.Bytes()
		}
	}
	return data, changed
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWord(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func gitOut(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-C", repo, "-c", "safe.directory=" + repo, "-c", "core.quotePath=off"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C", "GIT_CONFIG_NOSYSTEM=1"}
	out, errb := limitedOutput{limit: 64 << 20}, limitedOutput{limit: 4 << 10}
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(errb.buf.String()))
	}
	return out.buf.Bytes(), nil
}

type limitedOutput struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, errors.New("Git export exceeds output budget")
	}
	return b.buf.Write(p)
}
