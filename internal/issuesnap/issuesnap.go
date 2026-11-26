// Package issuesnap gathers the facts a problem card needs at the moment it
// is filed (owner 26.11: «метрики, JSON — чтобы была полная картина»), so the
// AI that fixes it later does not reconstruct the state by hand.
package issuesnap

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Reader is one read-only MCPshka tool.
type Reader func(context.Context, map[string]any) (any, error)

const (
	perCall  = 5 * time.Second
	section  = 2 << 10
	maxSpans = 15
)

// Build returns a markdown section with compact JSON facts: server version,
// host health, gateway readiness, the latest failed tool calls and, for a
// project, its context (HEAD, lease, jobs). A source that fails is named,
// never silently left out.
func Build(ctx context.Context, version, project string, readers map[string]Reader, now time.Time) string {
	type part struct {
		name string
		args map[string]any
		pick func(any) any
	}
	parts := []part{
		{"health_snapshot", map[string]any{}, nil},
		{"gateway_status", map[string]any{}, nil},
		{"tool_usage", map[string]any{"spans": 200}, failedSpans},
	}
	if path := projectPath(project); path != "" {
		parts = append(parts, part{"project_context", map[string]any{"path": path}, nil})
	}
	facts := map[string]any{"version": version, "at": now.UTC().Format(time.RFC3339)}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range parts {
		read := readers[p.name]
		if read == nil {
			facts[p.name] = "unavailable: tool not registered"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, perCall)
			defer cancel()
			v, err := read(c, p.args)
			var out any = v
			switch {
			case err != nil:
				out = "unavailable: " + clip(err.Error(), 300)
			case p.pick != nil:
				out = p.pick(v)
			}
			mu.Lock()
			facts[p.name] = bounded(out)
			mu.Unlock()
		}()
	}
	wg.Wait()
	raw, err := json.MarshalIndent(facts, "", " ")
	if err != nil {
		return ""
	}
	return "\n\n### Снимок при создании (автоматически)\n\n```json\n" + string(raw) + "\n```\n"
}

// projectPath maps a card's project to its repository; empty is MCPshka.
func projectPath(project string) string {
	p := strings.ToLower(strings.TrimSpace(project))
	if p == "" || p == "mcpshka" {
		return "/root/repos/mcpshka"
	}
	if strings.ContainsAny(p, `/\.`) || len(p) > 64 {
		return ""
	}
	return "/root/repos/" + p
}

// failedSpans keeps only the latest failed calls of tool_usage.
func failedSpans(v any) any {
	var u struct {
		Spans []map[string]any `json:"spans"`
	}
	raw, _ := json.Marshal(v)
	if json.Unmarshal(raw, &u) != nil {
		return "unreadable"
	}
	var out []map[string]any
	for i := len(u.Spans) - 1; i >= 0 && len(out) < maxSpans; i-- {
		if s := u.Spans[i]; s["outcome"] != "ok" {
			out = append(out, s)
		}
	}
	return out
}

// bounded keeps a section small: JSON longer than the limit becomes its head.
func bounded(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return "unreadable"
	}
	if len(raw) <= section {
		return v
	}
	return clip(string(raw), section) + "…"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
