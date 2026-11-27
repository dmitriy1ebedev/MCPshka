package issuesnap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildNamesEveryFactAndFailure(t *testing.T) {
	readers := map[string]Reader{
		"health_snapshot": func(context.Context, map[string]any) (any, error) { return map[string]any{"mem_free_mb": 113}, nil },
		"gateway_status":  func(context.Context, map[string]any) (any, error) { return nil, errors.New("broker timeout") },
		"tool_usage": func(context.Context, map[string]any) (any, error) {
			return map[string]any{"spans": []any{map[string]any{"tool": "fs_read", "outcome": "ok"}, map[string]any{"tool": "code_run", "outcome": "error"}}}, nil
		},
		"project_context": func(_ context.Context, a map[string]any) (any, error) { return map[string]any{"path": a["path"]}, nil },
	}
	out := Build(context.Background(), "0.19.3", "venta", readers, time.Unix(0, 0))
	for _, want := range []string{"0.19.3", "mem_free_mb", "unavailable: broker timeout", "code_run", "/root/repos/venta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "fs_read") {
		t.Fatal("successful calls are noise in a card")
	}
	if projectPath("../etc") != "" {
		t.Fatal("project name must not escape /root/repos")
	}
}
