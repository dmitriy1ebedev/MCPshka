package freellm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/issues"
	"mcpshka/internal/llmgateway"
)

const FixRoot = "/root/repos/mcpshka"

var cardID = regexp.MustCompile(`^(MCP|PRJ)-[0-9]{3,5}$`)
var codePath = regexp.MustCompile(`\b(?:internal|cmd|laned|packaging|docs)/[a-zA-Z0-9_./-]+\.(?:go|py|sh|md|json|yaml|yml)\b`)

type Edit struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}
type Proposal struct {
	Edits []Edit `json:"edits"`
}
type Fix struct {
	Project  string     `json:"project,omitempty"`
	Root     string     `json:"root,omitempty"`
	Steps    [][]string `json:"steps,omitempty"`
	Packages []string   `json:"packages,omitempty"`
	Card     string     `json:"card"`
	State    string     `json:"state"`
	Branch   string     `json:"branch"`
	Path     string     `json:"path,omitempty"`
	Base     string     `json:"base,omitempty"`
	Commit   string     `json:"commit,omitempty"`
	Test     string     `json:"test,omitempty"`
	Diff     string     `json:"diff,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Mode     string     `json:"mode"`
	Quorum   bool       `json:"quorum"`
	At       time.Time  `json:"at"`
}
type Conveyor struct {
	Dir     string
	Invoke  func(context.Context, string, map[string]any) (any, error)
	Compare func(context.Context, string) (llmgateway.Comparison, error)
	Now     func() time.Time
	Quiet   func() bool
	mu      sync.Mutex
}

func (c *Conveyor) call(ctx context.Context, name string, in map[string]any, out any) error {
	v, err := c.Invoke(ctx, name, in)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (c *Conveyor) save(f Fix) error {
	if !cardID.MatchString(f.Card) {
		return errors.New("invalid card id")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(c.Dir, 0700); err != nil {
		return err
	}
	p := filepath.Join(c.Dir, f.Card+".json")
	if err = os.WriteFile(p+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
func (c *Conveyor) List() ([]Fix, error) {
	entries, err := os.ReadDir(c.Dir)
	if os.IsNotExist(err) {
		return []Fix{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 256 {
		return nil, errors.New("fix queue exceeds limit")
	}
	out := []Fix{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var f Fix
		if json.Unmarshal(raw, &f) != nil || !cardID.MatchString(f.Card) {
			return nil, errors.New("invalid fix receipt")
		}
		out = append(out, f)
	}
	return out, nil
}

// Run works the open cards. "manual" is the owner's call and runs at any time;
// "night" is the scheduler's call and only runs 22:00-06:00 on a quiet host.
// cards, when given, limits the run to those card ids. Merging always waits for
// fixes_review.
func (c *Conveyor) Run(ctx context.Context, mode string, cards ...string) ([]Fix, error) {
	return c.run(ctx, mode, nil, cards...)
}

func (c *Conveyor) run(ctx context.Context, mode string, observe func(Fix, Fix), cards ...string) ([]Fix, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode == "" {
		mode = "manual"
	}
	now := c.Now()
	if mode == "night" {
		if now.Hour() >= 6 && now.Hour() < 22 {
			return nil, errors.New("NIGHT_NOT_QUIET: scheduled run only during 22:00-06:00")
		}
		mode = "manual"
	}
	if mode != "manual" {
		return nil, errors.New("merging requires morning fixes_review; mode must be manual")
	}
	// Manual runs may start at any hour but must respect the same H96 load
	// budget as scheduled runs. Missing load evidence fails closed.
	if c.Quiet == nil || !c.Quiet() {
		return nil, errors.New("NIGHT_NOT_QUIET: host is busy or load probe unavailable")
	}
	only := map[string]bool{}
	for _, id := range cards {
		only[strings.ToUpper(strings.TrimSpace(id))] = true
	}
	existing, err := c.List()
	if err != nil {
		return nil, err
	}
	known := map[string]Fix{}
	for _, f := range existing {
		known[f.Card] = f
	}
	var list struct {
		Issues []issues.Summary `json:"issues"`
	}
	if err := c.call(ctx, "issue_list", map[string]any{"status": "active", "limit": 500}, &list); err != nil {
		return nil, err
	}
	out := []Fix{}
	prioritize(list.Issues)
	for _, summary := range list.Issues {
		if summary.Priority != "P1" && summary.Priority != "P2" && summary.Priority != "P3" || !cardID.MatchString(summary.ID) || summary.Status != "open" && summary.Status != "in_progress" {
			continue
		}
		if len(only) > 0 && !only[summary.ID] {
			continue
		}
		if prev, ok := known[summary.ID]; ok {
			before := prev
			if prev.State == "testing" {
				token, e := os.ReadFile(filepath.Join(c.Dir, prev.Card+".lease-token"))
				if e != nil {
					return out, e
				}
				prev, err = c.finish(ctx, prev, string(token))
				if err != nil {
					return out, err
				}
			}
			if observe != nil {
				observe(before, prev)
			}
			out = append(out, prev)
			continue
		}
		var card issues.Issue
		if err := c.call(ctx, "issue_get", map[string]any{"id": summary.ID}, &card); err != nil {
			return out, err
		}
		project, root := projectRoot(card.Project)
		if root == "" {
			continue
		}
		paths := codePath.FindAllString(card.Body, -1)
		f := Fix{Project: project, Root: root, Card: card.ID, Branch: "fix/" + strings.ToLower(card.ID), State: "preparing", Mode: mode, At: now}
		if err := c.save(f); err != nil {
			return out, err
		}
		f, err = c.prepare(ctx, f, card, paths)
		if err != nil {
			if f.Test == "" && f.State != "dispatching_test" {
				f.State = "blocked"
			}
			f.Reason = llmgateway.Redact(err.Error()).Text
			if e := c.save(f); e != nil {
				return out, e
			}
		}
		if observe != nil {
			observe(Fix{}, f)
		}
		out = append(out, f)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, nil
}
func safeFixPath(p string) bool {
	if !filepath.IsLocal(p) || filepath.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(p), "/") {
		if part == ".git" || strings.HasPrefix(part, ".env") || strings.Contains(part, "secret") || strings.Contains(part, "credential") || strings.Contains(part, "vault") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") || part == "agents.md" || part == "roadmap.md" {
			return false
		}
	}
	return true
}
