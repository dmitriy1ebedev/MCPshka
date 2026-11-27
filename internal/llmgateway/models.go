package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	g.loadHealth()
	for i, m := range list {
		if h, ok := g.health[c.BaseURL+"\n"+m.ID]; ok {
			m.State, m.CheckedAt, m.LastError = h.State, h.CheckedAt, h.LastError
			if time.Since(h.CheckedAt) >= healthTTL(c, h) {
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
		if len(m.LastError) > 400 {
			m.LastError = m.LastError[:400]
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadHealth()
	g.health[c.BaseURL+"\n"+id] = m
	g.saveHealth()
}

// removedTTL: a model the router reports as not found or removed stays
// skipped for a day, not the few minutes a 503 or a timeout earns.
const removedTTL = 24 * time.Hour

var removed = regexp.MustCompile(`(?i)model_not_found|not found or removed|model unavailable|does not exist`)

func healthTTL(c Config, h Model) time.Duration {
	if h.State == "dead" && removed.MatchString(h.LastError) {
		return removedTTL
	}
	return time.Duration(c.HealthCacheMinutes) * time.Minute
}

// loadHealth reads the shared probe cache once per process (g.mu held).
func (g *Gateway) loadHealth() {
	if g.health == nil {
		g.health = map[string]Model{}
	}
	if g.healthRead || g.HealthPath == "" {
		return
	}
	g.healthRead = true
	if raw, err := os.ReadFile(g.HealthPath); err == nil {
		var saved map[string]Model
		if json.Unmarshal(raw, &saved) == nil {
			for k, v := range saved {
				if _, ok := g.health[k]; !ok && time.Since(v.CheckedAt) < removedTTL {
					g.health[k] = v
				}
			}
		}
	}
}

// saveHealth writes the cache atomically; a failure only costs re-probes.
func (g *Gateway) saveHealth() {
	if g.HealthPath == "" {
		return
	}
	raw, err := json.Marshal(g.health)
	if err != nil || os.MkdirAll(filepath.Dir(g.HealthPath), 0o750) != nil {
		return
	}
	tmp := g.HealthPath + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, g.HealthPath)
	}
}
