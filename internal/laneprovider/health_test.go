package laneprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHealthReadsSnapshotWithoutProbeAuthority(t *testing.T) {
	calls := 0
	s := newService(func(ctx context.Context, op string, args map[string]any) (json.RawMessage, error) {
		calls++
		if op != "health" || len(args) != 0 {
			t.Fatal("not read-only health")
		}
		return json.RawMessage(`{"ok":true,"state":{"version":1,"mode":"shadow","updated_at":"2026-10-09T16:00:00Z","paths":{"private-path":{"secret":"never-export"}}}}`), nil
	})
	h, e := s.Health(context.Background())
	if e != nil || h.PathCount != 1 || h.Authority != "lane.health" || calls != 1 {
		t.Fatalf("%+v %v", h, e)
	}
	raw, _ := json.Marshal(h)
	if string(raw) == "" || strings.Contains(string(raw), "never-export") {
		t.Fatal("missing summary")
	}
}
func TestHealthRejectsUnknownSchema(t *testing.T) {
	for _, raw := range []string{`{"ok":true}`, `{"ok":false,"state":{"version":1}}`, `{"ok":true,"state":{"version":2}}`} {
		s := newService(func(context.Context, string, map[string]any) (json.RawMessage, error) {
			return json.RawMessage(raw), nil
		})
		if _, e := s.Health(context.Background()); e == nil {
			t.Fatal("accepted unknown schema")
		}
	}
}
