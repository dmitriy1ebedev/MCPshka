package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/toolstats"
	"testing"
	"time"
)

func TestToolUsageWorkTrendsThroughTool(t *testing.T) {
	m, _, _ := yieldFixture(t)
	now := time.Now().UTC()
	put := func(k string, v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if err := m.Database().Put(k, raw); err != nil {
			t.Fatal(err)
		}
	}
	put("selfheal/state.json", map[string]any{"incidents": []map[string]any{
		{"At": now.Add(-time.Hour), "Result": "detected"},
		{"At": now.Add(-time.Hour), "Result": "restart reserved"},
		{"At": now.Add(-30 * time.Minute), "Result": "recovered"},
		{"At": now.Add(-10 * 24 * time.Hour), "Result": "detected"},
	}})
	put("missions/sample.json", map[string]any{"state": "BLOCKED", "history": []map[string]any{
		{"At": now.Add(-time.Hour), "Action": "PAUSED", "State": "BLOCKED"},
		{"At": now.Add(-time.Hour + 2*time.Minute), "Action": "resume", "State": "RUNNING"},
	}, "blockers": []map[string]any{{"reason": "quota exhausted; backoff"}}})
	put("release-queue/v1/sample.json", map[string]any{"Stage": "VERIFY"})
	old := toolstats.Default
	toolstats.Default = toolstats.New(now)
	t.Cleanup(func() { toolstats.Default = old })
	toolstats.Default.Resolution([]string{"fs_read"}, now.Add(-20*time.Minute))
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerToolUsage(r, m.Database())
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"tool_usage"}})
	v, err := r.readers["tool_usage"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	w := v.(ToolUsageResult).Work
	o := w.Outcomes
	if len(o.Windows) != 2 || o.Windows[0].Incidents != 1 || o.Windows[1].Incidents != 2 || o.Windows[0].RecoveryRate == nil || *o.Windows[0].RecoveryRate != 1 || o.Windows[0].ManualInterventions != 2 || o.MedianNextSafeStepSeconds == nil || *o.MedianNextSafeStepSeconds != 120 || o.DeployWithoutVerify != 1 || o.QuotaHolds != 1 || o.BackoffHolds != 1 || w.Discoverability.ResolveWithoutInvoke != 1 {
		t.Fatal(w)
	}
}
