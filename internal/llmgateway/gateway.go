// Package llmgateway sends small, filtered texts to an OpenAI-compatible
// endpoint (a self-hosted FreeLLMAPI or any free-tier provider) for cheap
// work the main model should not spend context on: summarising a big tool
// result inside code_run, classifying journals, the night review pilot.
// Nothing leaves the host without Filter; every send is journalled (who,
// bytes, sha256, verdict) and never the text itself. No config — no sends.
package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultConfigPath = "/etc/mcpshka/llm.json"
	maxReply          = 256 << 10
)

// Config is /etc/mcpshka/llm.json. KeyFile is optional (a local FreeLLMAPI
// may not need one) and must be 0600 when present.
type Config struct {
	BaseURL             string `json:"base_url"`
	Model               string `json:"model"`
	KeyFile             string `json:"key_file,omitempty"`
	MaxInputBytes       int    `json:"max_input_bytes,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds,omitempty"`
	BudgetSeconds       int    `json:"budget_seconds,omitempty"`
	ProbeTimeoutSeconds int    `json:"probe_timeout_seconds,omitempty"`
	HealthCacheMinutes  int    `json:"health_cache_minutes,omitempty"`
	// OAuth exchanges the key for a short-lived bearer (GigaChat style);
	// without it the key itself is the bearer.
	OAuth *OAuth `json:"oauth,omitempty"`
	// Dialect of function calling: "" (OpenAI tools) or "gigachat" (functions).
	Dialect string `json:"dialect,omitempty"`
	// Concurrency caps simultaneous requests to this endpoint (GigaChat
	// Freemium: 1 stream); 0 means unlimited.
	Concurrency int `json:"concurrency,omitempty"`
	// BrainModel marks the endpoint as MCPshka's brain (function calling):
	// Converse tries such endpoints first, with this model.
	BrainModel string `json:"brain_model,omitempty"`
	// Fallback endpoints are tried in order when this one yields nothing
	// (e.g. a self-hosted FreeLLMAPI first, then GigaChat). No nesting.
	Fallback []Config `json:"fallback,omitempty"`
}

// Gateway is safe for concurrent use. Secrets overrides the known-secret
// source (tests); nil reads SecretSources.
type Gateway struct {
	ConfigPath  string
	JournalPath string
	// HealthPath keeps model probes across restarts. A dead mark lived only
	// health_cache_minutes in memory, so nearly every llm.compare re-probed
	// models the router had removed (26.11: 11 of 12 failed, the budget went
	// on 404s); removed models now stay skipped for a day.
	HealthPath string
	healthRead bool
	HTTP       *http.Client
	Secrets    func() []string
	mu         sync.Mutex
	health     map[string]Model
	catalogs   map[string]catalog
	tokens     map[string]token
	slots      map[string]chan struct{}
}

// slot waits for a free request slot of an endpoint with a concurrency cap.
func (g *Gateway) slot(ctx context.Context, c Config) (func(), error) {
	if c.Concurrency <= 0 {
		return func() {}, nil
	}
	g.mu.Lock()
	if g.slots == nil {
		g.slots = map[string]chan struct{}{}
	}
	ch, ok := g.slots[c.BaseURL]
	if !ok || cap(ch) != c.Concurrency {
		ch = make(chan struct{}, c.Concurrency)
		g.slots[c.BaseURL] = ch
	}
	g.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, errors.New("LLM endpoint busy (concurrency limit)")
	}
}

type catalog struct {
	models []Model
	at     time.Time
}

func (g *Gateway) secrets() []string {
	if g.Secrets != nil {
		return g.Secrets()
	}
	return knownSecrets()
}

var ErrUnavailable = errors.New("LLM_UNAVAILABLE")

// Send is one journal line; the text itself is never written.
type Send struct {
	At         string   `json:"at"`
	Host       string   `json:"host"`
	Model      string   `json:"model"`
	Purpose    string   `json:"purpose"`
	Bytes      int      `json:"bytes"`
	SHA256     string   `json:"sha256"`
	Allowed    bool     `json:"allowed"`
	Reasons    []string `json:"reasons,omitempty"`
	Redactions int      `json:"redactions"`
	Status     string   `json:"status"`
	MS         int64    `json:"ms"`
}

func (g *Gateway) config() (Config, error) {
	path := g.ConfigPath
	if path == "" {
		path = DefaultConfigPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%w: no %s (base_url, model)", ErrUnavailable, path)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%w: %s: %v", ErrUnavailable, path, err)
	}
	if err := c.normalize(); err != nil {
		return c, err
	}
	for i := range c.Fallback {
		if len(c.Fallback[i].Fallback) > 0 {
			return c, fmt.Errorf("%w: fallback endpoints cannot nest", ErrUnavailable)
		}
		if err := c.Fallback[i].normalize(); err != nil {
			return c, err
		}
	}
	return c, nil
}

// endpoints is the primary followed by its fallbacks.
func (c Config) endpoints() []Config {
	return append([]Config{c}, c.Fallback...)
}

func (c *Config) normalize() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || c.Model == "" {
		return fmt.Errorf("%w: base_url must be http(s)://host[/v1], model required", ErrUnavailable)
	}
	if c.MaxInputBytes <= 0 || c.MaxInputBytes > 256<<10 {
		c.MaxInputBytes = 64 << 10
	}
	if c.TimeoutSeconds <= 0 || c.TimeoutSeconds > 300 {
		c.TimeoutSeconds = 90
	}
	if c.BudgetSeconds <= 0 || c.BudgetSeconds > 600 {
		c.BudgetSeconds = 180
	}
	if c.ProbeTimeoutSeconds <= 0 || c.ProbeTimeoutSeconds > 15 {
		c.ProbeTimeoutSeconds = 5
	}
	if c.HealthCacheMinutes <= 0 || c.HealthCacheMinutes > 60 {
		c.HealthCacheMinutes = 5
	}
	if c.Dialect != "" && c.Dialect != "openai" && c.Dialect != "gigachat" {
		return fmt.Errorf("%w: dialect must be openai or gigachat", ErrUnavailable)
	}
	if c.OAuth != nil {
		if err := c.OAuth.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) key() (string, error) {
	if c.KeyFile == "" {
		return "", nil
	}
	info, err := os.Stat(c.KeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: key_file must be a private regular file", ErrUnavailable)
	}
	raw, err := os.ReadFile(c.KeyFile)
	return strings.TrimSpace(string(raw)), err
}

func (g *Gateway) journal(rec Send) {
	if g.JournalPath == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(g.JournalPath), 0o700)
	if info, err := os.Stat(g.JournalPath); err == nil && info.Size() > 4<<20 {
		_ = os.Rename(g.JournalPath, g.JournalPath+".1")
	}
	f, err := os.OpenFile(g.JournalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(rec)
	_, _ = f.Write(append(line, '\n'))
}
