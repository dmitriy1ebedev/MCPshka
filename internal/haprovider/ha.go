// Package haprovider is a bounded Home Assistant REST client for MCPshka.
// The long-lived token lives in MCPshka state, never in tool arguments or
// replies; every response is size-capped and marked untrusted.
package haprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultURL       = "http://127.0.0.1:8123"
	DefaultTokenPath = "/var/lib/mcpshka/secrets/ha.token"
	maxBody          = 4 << 20
	maxStates        = 500
)

var (
	entityPattern  = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z0-9_]+$`)
	servicePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

type Client struct {
	URL       string
	TokenPath string
	HTTP      *http.Client
}

func New() *Client {
	url := strings.TrimRight(os.Getenv("MCPSHKA_HA_URL"), "/")
	if url == "" {
		url = DefaultURL
	}
	path := os.Getenv("MCPSHKA_HA_TOKEN_FILE")
	if path == "" {
		path = DefaultTokenPath
	}
	return &Client{URL: url, TokenPath: path, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type State struct {
	EntityID     string         `json:"entity_id"`
	State        string         `json:"state"`
	FriendlyName string         `json:"friendly_name,omitempty"`
	LastChanged  string         `json:"last_changed,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

type StatesResult struct {
	Trust     string  `json:"trust"`
	Total     int     `json:"total"`
	States    []State `json:"states"`
	Truncated bool    `json:"truncated"`
}

func (c *Client) token() (string, error) {
	info, err := os.Stat(c.TokenPath)
	if err != nil {
		return "", fmt.Errorf("Home Assistant token unavailable at %s: %w", c.TokenPath, err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("Home Assistant token file must be private (0600)")
	}
	raw, err := os.ReadFile(c.TokenPath)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(raw))
	if t == "" || len(t) > 4096 {
		return "", errors.New("Home Assistant token file is empty or oversized")
	}
	return t, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	token, err := c.token()
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Home Assistant unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		return errors.New("Home Assistant response exceeds 4 MiB")
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("Home Assistant %s %s: HTTP %d %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func compact(s State, full bool) State {
	if name, ok := s.Attributes["friendly_name"].(string); ok {
		s.FriendlyName = name
	}
	if !full {
		s.Attributes = nil
	}
	return s
}

// States lists entities, optionally by domain prefix and a case-insensitive
// substring of entity id or friendly name, without attributes.
func (c *Client) States(ctx context.Context, domain, query string, limit int) (StatesResult, error) {
	if domain != "" && !servicePattern.MatchString(domain) {
		return StatesResult{}, errors.New("invalid domain")
	}
	if limit <= 0 || limit > maxStates {
		limit = 100
	}
	var all []State
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &all); err != nil {
		return StatesResult{}, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := StatesResult{Trust: "UNTRUSTED", States: []State{}}
	sort.Slice(all, func(i, j int) bool { return all[i].EntityID < all[j].EntityID })
	for _, s := range all {
		s = compact(s, false)
		if domain != "" && !strings.HasPrefix(s.EntityID, domain+".") {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.EntityID+" "+s.FriendlyName), q) {
			continue
		}
		out.Total++
		if len(out.States) < limit {
			out.States = append(out.States, s)
		} else {
			out.Truncated = true
		}
	}
	return out, nil
}

// State returns one entity with attributes.
func (c *Client) State(ctx context.Context, entity string) (State, error) {
	if !entityPattern.MatchString(entity) {
		return State{}, errors.New("invalid entity_id")
	}
	var s State
	if err := c.do(ctx, http.MethodGet, "/api/states/"+entity, nil, &s); err != nil {
		return State{}, err
	}
	return compact(s, true), nil
}

type CheckResult struct {
	Result string `json:"result"`
	Errors string `json:"errors,omitempty"`
}

// CheckConfig validates configuration.yaml without applying it.
func (c *Client) CheckConfig(ctx context.Context) (CheckResult, error) {
	var out struct {
		Result string  `json:"result"`
		Errors *string `json:"errors"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/config/core/check_config", nil, &out); err != nil {
		return CheckResult{}, err
	}
	r := CheckResult{Result: out.Result}
	if out.Errors != nil {
		r.Errors = *out.Errors
	}
	return r, nil
}

type CallResult struct {
	Domain  string  `json:"domain"`
	Service string  `json:"service"`
	Changed []State `json:"changed"`
}

// CallService invokes one service; reload_* and homeassistant.* reloads are
// ordinary services. Attributes of changed entities are omitted.
func (c *Client) CallService(ctx context.Context, domain, service string, data map[string]any) (CallResult, error) {
	if !servicePattern.MatchString(domain) || !servicePattern.MatchString(service) {
		return CallResult{}, errors.New("invalid domain or service")
	}
	if domain == "homeassistant" && (service == "stop" || service == "restart") {
		return CallResult{}, errors.New("stopping or restarting Home Assistant is an owner action; use docker_action restart homeassistant")
	}
	if data == nil {
		data = map[string]any{}
	}
	var changed []State
	if err := c.do(ctx, http.MethodPost, "/api/services/"+domain+"/"+service, data, &changed); err != nil {
		return CallResult{}, err
	}
	out := CallResult{Domain: domain, Service: service, Changed: []State{}}
	for i, s := range changed {
		if i >= 50 {
			break
		}
		out.Changed = append(out.Changed, compact(s, false))
	}
	return out, nil
}
