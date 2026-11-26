package llmgateway

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// 26.11: each llm.ask/compare is its own process; probes must survive it,
// and a removed model stays skipped for a day.
func TestHealthSharedBetweenProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	c := Config{BaseURL: "http://gw", HealthCacheMinutes: 5}
	first := &Gateway{HealthPath: path}
	first.mark(c, "gone", errors.New("LLM status 404: model_not_found"))
	first.mark(c, "busy", errors.New("LLM status 503: high demand"))
	first.mark(c, "ok", nil)

	second := &Gateway{HealthPath: path}
	second.mu.Lock()
	second.loadHealth()
	got := second.health
	second.mu.Unlock()
	if got["http://gw\ngone"].State != "dead" || got["http://gw\nok"].State != "alive" {
		t.Fatalf("%+v", got)
	}
	old := got["http://gw\ngone"]
	old.CheckedAt = time.Now().Add(-time.Hour)
	if healthTTL(c, old) != removedTTL || time.Since(old.CheckedAt) >= healthTTL(c, old) {
		t.Fatal("a removed model must stay dead for a day")
	}
	busy := got["http://gw\nbusy"]
	busy.CheckedAt = time.Now().Add(-10 * time.Minute)
	if time.Since(busy.CheckedAt) < healthTTL(c, busy) {
		t.Fatal("a 503 must be re-probed after the short cache")
	}
}
