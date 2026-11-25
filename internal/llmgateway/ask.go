package llmgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"
)

type Options struct {
	Model          string `json:"model,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	BudgetSeconds  int    `json:"budget_seconds,omitempty"`
	Count          int    `json:"count,omitempty"`
}

type Reply struct {
	Text           string         `json:"text"`
	Model          string         `json:"model"`
	Redactions     int            `json:"redactions"`
	RedactionTypes map[string]int `json:"redaction_types"`
}

type Attempt struct {
	Model string `json:"model"`
	Error string `json:"error"`
}

type Comparison struct {
	Replies   []Reply   `json:"replies"`
	Failures  []Attempt `json:"failures"`
	Requested int       `json:"requested"`
	Complete  bool      `json:"complete"`
}

// Ask is the text-only compatibility entry point. New callers use AskDetailed.
func (g *Gateway) Ask(ctx context.Context, purpose, system, user string, model ...string) (string, error) {
	opts := Options{}
	if len(model) == 1 {
		opts.Model = model[0]
	}
	r, err := g.AskDetailed(ctx, purpose, system, user, opts)
	return r.Text, err
}

func (g *Gateway) AskDetailed(ctx context.Context, purpose, system, user string, opts Options) (Reply, error) {
	result, err := g.run(ctx, purpose, system, user, opts, 1)
	if err != nil {
		return Reply{}, err
	}
	if len(result.Replies) == 0 {
		raw := "LLM no live model answered"
		for _, f := range result.Failures {
			if len(raw) < 1600 {
				raw += "; " + f.Model + ": " + f.Error
			}
		}
		return Reply{}, errors.New(raw)
	}
	return result.Replies[0], nil
}

// Compare returns independent answers and explicit partial-quorum evidence.
// Choosing a code patch remains the controller's job.
func (g *Gateway) Compare(ctx context.Context, purpose, system, user string, opts Options) (Comparison, error) {
	n := opts.Count
	if n == 0 {
		n = 3
	}
	if n < 1 || n > 5 {
		return Comparison{}, errors.New("count must be 1..5")
	}
	return g.run(ctx, purpose, system, user, opts, n)
}

func (g *Gateway) run(ctx context.Context, purpose, system, user string, opts Options, n int) (Comparison, error) {
	out := Comparison{Replies: []Reply{}, Failures: []Attempt{}, Requested: n}
	c, err := g.config()
	if err != nil {
		return out, err
	}
	if opts.Model != "" && !validModel(opts.Model) {
		return out, errors.New("invalid model name")
	}
	for _, setting := range []struct{ value, max int }{{opts.TimeoutSeconds, 300}, {opts.BudgetSeconds, 600}} {
		if setting.value < 0 || setting.value > setting.max {
			return out, fmt.Errorf("timeout/budget must be 1..%d seconds or 0 for config", setting.max)
		}
	}
	if opts.BudgetSeconds > 0 {
		c.BudgetSeconds = opts.BudgetSeconds
	}
	if len(system)+len(user)+2 > c.MaxInputBytes {
		return out, errors.New("LIMIT: LLM input too large; summarise first")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.BudgetSeconds)*time.Second)
	defer cancel()
	answered := map[string]bool{}
	for i, ep := range c.endpoints() {
		if opts.TimeoutSeconds > 0 {
			ep.TimeoutSeconds = opts.TimeoutSeconds
		}
		if i > 0 && len(system)+len(user)+2 > ep.MaxInputBytes {
			out.Failures = append(out.Failures, Attempt{Model: ep.BaseURL, Error: "input over this endpoint's limit"})
			continue
		}
		err := g.runEndpoint(ctx, ep, purpose, system, user, opts.Model, n, &out, answered)
		if err != nil && len(c.Fallback) == 0 {
			return out, err
		}
		if err != nil {
			out.Failures = append(out.Failures, Attempt{Model: ep.BaseURL, Error: err.Error()})
		}
		if len(out.Replies) == n || ctx.Err() != nil {
			break
		}
	}
	out.Complete = len(out.Replies) == n
	return out, nil
}

// runEndpoint asks one endpoint's models in preference order until n
// distinct replies are collected in out.
func (g *Gateway) runEndpoint(ctx context.Context, c Config, purpose, system, user, model string, n int, out *Comparison, answered map[string]bool) error {
	key, err := c.key()
	if err != nil {
		return err
	}
	vs, vu := g.redact(system, key), g.redact(user, key)
	types := map[string]int{}
	for kind, v := range vs.Types {
		types[kind] += v
	}
	for kind, v := range vu.Types {
		types[kind] += v
	}
	models, err := g.models(ctx, c, key)
	if err != nil {
		return err
	}
	preferred := model
	if preferred == "" {
		preferred = c.Model
	}
	sort.SliceStable(models, func(i, j int) bool {
		if (models[i].ID == preferred) != (models[j].ID == preferred) {
			return models[i].ID == preferred
		}
		return models[i].State == "alive" && models[j].State != "alive"
	})
	for _, m := range models {
		if ctx.Err() != nil {
			out.Failures = append(out.Failures, Attempt{Error: "LLM total budget exhausted"})
			break
		}
		if m.State == "dead" {
			out.Failures = append(out.Failures, Attempt{m.ID, "cached: " + m.LastError})
			continue
		}
		if m.ExecutionStatus != "" && m.ExecutionStatus != "ready" {
			out.Failures = append(out.Failures, Attempt{m.ID, "provider reports model unavailable"})
			continue
		}
		if m.State != "alive" {
			_, err = g.attempt(ctx, c, key, m.ID, purpose+":probe", "", "Reply OK.", true, 0, nil)
			if err != nil {
				g.mark(c, m.ID, err)
				out.Failures = append(out.Failures, Attempt{m.ID, err.Error()})
				continue
			}
			g.mark(c, m.ID, nil)
		}
		reply, err := g.attempt(ctx, c, key, m.ID, purpose, vs.Text, vu.Text, false, vs.Redactions+vu.Redactions, types)
		g.mark(c, m.ID, err)
		if err != nil {
			out.Failures = append(out.Failures, Attempt{m.ID, err.Error()})
			continue
		}
		// A router may return the same underlying model for distinct aliases.
		if answered[reply.Model] {
			out.Failures = append(out.Failures, Attempt{m.ID, "duplicate response model"})
			continue
		}
		answered[reply.Model] = true
		reply.Redactions, reply.RedactionTypes = vs.Redactions+vu.Redactions, types
		out.Replies = append(out.Replies, reply)
		if len(out.Replies) == n {
			break
		}
	}
	return nil
}

func (g *Gateway) attempt(ctx context.Context, c Config, key, model, purpose, system, user string, probe bool, redactions int, types map[string]int) (Reply, error) {
	seconds := c.TimeoutSeconds
	if probe {
		seconds = c.ProbeTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	start := time.Now()
	reply, err := g.complete(ctx, c, key, model, system, user, probe)
	sum := sha256.Sum256([]byte(system + "\n\n" + user))
	u, _ := url.Parse(c.BaseURL)
	status := "OK"
	if err != nil {
		status = "FAILED"
	}
	reasons := []string{}
	for kind := range types {
		reasons = append(reasons, kind)
	}
	sort.Strings(reasons)
	g.journal(Send{At: start.UTC().Format(time.RFC3339), Host: u.Host, Model: model, Purpose: purpose,
		Bytes: len(system) + len(user) + 2, SHA256: hex.EncodeToString(sum[:8]), Allowed: true,
		Reasons: reasons, Redactions: redactions, Status: status, MS: time.Since(start).Milliseconds()})
	return reply, err
}
