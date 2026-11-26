package hosttriage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// configDir is swapped by tests.
var configDir = "/etc/mcpshka"

var envKey = regexp.MustCompile(`^(export +)?[A-Za-z_][A-Za-z0-9_]*=`)

// configProblems is the config doctor (like /status in Claude Code): every
// MCPshka config file this process can read is parsed the way its loader
// reads it — *.json as JSON, *.env as KEY=VALUE lines. Only the file name and
// the place of the error leave: values may be secrets. Root-only files the
// server cannot read are skipped; their own service fails loudly instead.
// Backups (*.bak*) are history, not config.
func configProblems(dir string) []TriageProblem {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []TriageProblem
	for _, e := range entries {
		name := e.Name()
		ext := filepath.Ext(name)
		if e.IsDir() || strings.Contains(name, ".bak") || (ext != ".json" && ext != ".env") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrPermission) {
			continue
		}
		var bad string
		switch {
		case err != nil:
			bad = "unreadable"
		case len(raw) > 1<<20:
			bad = "larger than 1 MiB"
		case ext == ".json":
			bad = jsonError(raw)
		default:
			bad = envError(raw)
		}
		if bad == "" {
			continue
		}
		p := TriageProblem{Severity: "critical", Kind: "config", Subject: filepath.Join(dir, name), Evidence: bad,
			Action: &TriageAction{Tool: "owner", Approval: "owner"}, Note: "fix the file by hand; the service that reads it uses defaults or fails until then"}
		if b := newestBackup(dir, name, entries); b != "" {
			p.Note = "the newest backup is " + b + "; compare before restoring"
		}
		out = append(out, p)
	}
	return out
}

func jsonError(raw []byte) string {
	var v any
	err := json.Unmarshal(raw, &v)
	if err == nil {
		return ""
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		line := bytes.Count(raw[:min(int(syn.Offset), len(raw))], []byte("\n")) + 1
		return "invalid JSON at line " + strconv.Itoa(line)
	}
	return "invalid JSON"
}

func envError(raw []byte) string {
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || envKey.MatchString(line) {
			continue
		}
		return "line " + strconv.Itoa(i+1) + " is not KEY=VALUE"
	}
	return ""
}

func newestBackup(dir, name string, entries []os.DirEntry) string {
	var found []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(strings.TrimPrefix(n, "."), name+".bak") {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		a, _ := os.Stat(filepath.Join(dir, found[i]))
		b, _ := os.Stat(filepath.Join(dir, found[j]))
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	return found[0]
}
