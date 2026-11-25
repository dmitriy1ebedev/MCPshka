package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Model separates the provider's availability from an observed local probe.
type Model struct {
	ID              string    `json:"id"`
	ExecutionStatus string    `json:"execution_status,omitempty"`
	State           string    `json:"state"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

func concrete(id string) bool {
	return validModel(id) && id != "auto" && !strings.HasPrefix(id, "auto:") && id != "fusion" &&
		id != "default" && id != "opus" && id != "sonnet" && id != "haiku" && !strings.HasPrefix(id, "claude-") &&
		!strings.Contains(strings.ToLower(id), "embedding") // vectors, not chat (GigaChat lists both)
}

// Models uses only the configured gateway and its existing private key file.
// Discovery itself never triggers dozens of provider probes.
func (g *Gateway) Models(ctx context.Context) ([]Model, error) {
	c, err := g.config()
	if err != nil {
		return nil, err
	}
	key, err := c.key()
	if err != nil {
		return nil, err
	}
	return g.models(ctx, c, key)
}

func (g *Gateway) models(ctx context.Context, c Config, key string) ([]Model, error) {
	g.mu.Lock()
	entry, cached := g.catalogs[c.BaseURL]
	cached = cached && time.Since(entry.at) < time.Duration(c.HealthCacheMinutes)*time.Minute
	list := append([]Model(nil), entry.models...)
	g.mu.Unlock()
	if !cached {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		raw, err := g.request(probeCtx, c, key, http.MethodGet, "/models", nil)
		if err != nil {
			return nil, err
		}
		var out struct {
			Data []Model `json:"data"`
		}
		if json.Unmarshal(raw, &out) != nil || len(out.Data) == 0 {
			return nil, errors.New("LLM models catalog is empty or invalid")
		}
		list = nil
		seen := map[string]bool{}
		for _, m := range out.Data {
			if !concrete(m.ID) || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			list = append(list, Model{ID: m.ID, ExecutionStatus: m.ExecutionStatus, State: "unknown"})
		}
		g.mu.Lock()
		if g.catalogs == nil {
			g.catalogs = map[string]catalog{}
		}
		g.catalogs[c.BaseURL] = catalog{models: append([]Model(nil), list...), at: time.Now()}
		g.mu.Unlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, m := range list {
		if h, ok := g.health[c.BaseURL+"\n"+m.ID]; ok {
			m.State, m.CheckedAt, m.LastError = h.State, h.CheckedAt, h.LastError
			if time.Since(h.CheckedAt) >= time.Duration(c.HealthCacheMinutes)*time.Minute {
				m.State = "unknown"
			}
		}
		list[i] = m
	}
	return list, nil
}

func (g *Gateway) mark(c Config, id string, err error) {
	m := Model{ID: id, State: "alive", CheckedAt: time.Now().UTC()}
	if err != nil {
		m.State, m.LastError = "dead", err.Error()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.health == nil {
		g.health = map[string]Model{}
	}
	g.health[c.BaseURL+"\n"+id] = m
}
