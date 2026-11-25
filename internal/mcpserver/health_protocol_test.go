package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/runtimehealth"
	"testing"
)

func TestHealthNativeProtocolAcceptsObjectEvidence(t *testing.T) {
	for _, state := range []string{"ok", "unknown"} {
		t.Run(state, func(t *testing.T) {
			c := healthlayer.New([]healthlayer.Probe{{Name: "diag", Read: func(context.Context) (healthlayer.Sample, error) {
				return healthlayer.Sample{State: state, Data: json.RawMessage(`{"ready":true}`)}, nil
			}}})
			srv := mcp.NewServer(&mcp.Implementation{Name: "health-protocol", Version: "1"}, nil)
			r := &toolRegistry{server: srv, tools: map[string]bool{}, readers: map[string]probeHandler{}}
			registerHealthTools(r, c)
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			ctx := context.Background()
			ss, e := srv.Connect(ctx, serverTransport, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer ss.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "health-client", Version: "1"}, nil)
			cs, e := client.Connect(ctx, clientTransport, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer cs.Close()
			result, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "health_snapshot", Arguments: map[string]any{}})
			if e != nil || result.IsError {
				t.Fatalf("native health output rejected: %+v %v", result, e)
			}
			raw, e := json.Marshal(result.StructuredContent)
			if e != nil || !json.Valid(raw) {
				t.Fatal("invalid native evidence")
			}
			var evidence healthlayer.Report
			if e := json.Unmarshal(raw, &evidence); e != nil || evidence.State != state || len(evidence.Items) != 1 || state == "ok" && (len(evidence.Baseline) != 1 || evidence.Baseline[0].Name != "diag.ready" || evidence.Baseline[0].Samples != 1) || state == "unknown" && len(evidence.Baseline) != 0 {
				t.Fatalf("rollup missing from MCP structured response: %+v %v", evidence, e)
			}
			cached, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "health_snapshot", Arguments: map[string]any{}})
			if err != nil || cached.IsError {
				t.Fatalf("cached read: %v", err)
			}
			raw, _ = json.Marshal(cached.StructuredContent)
			var again healthlayer.Report
			if err := json.Unmarshal(raw, &again); err != nil || !again.Cached || len(again.Baseline) != len(evidence.Baseline) {
				t.Fatalf("cache lost baseline: %+v %v", again, err)
			}
			if len(again.Baseline) > 0 && again.Baseline[0].Samples != 1 {
				t.Fatal("cached read added a sample")
			}
			if state != "ok" && evidence.Items[0].NextSafeAction == nil {
				t.Fatal("protocol lost next_safe_action")
			}
		})
	}
}

func TestHealthNativeDeploymentRegistry(t *testing.T) {
	m, _, _ := yieldFixture(t)
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			if err := m.Database().Put("deploys/1792159092901-aaaaaaaaaaaa.json", []byte("{")); err != nil {
				t.Fatal(err)
			}
		}
		c := healthlayer.New([]healthlayer.Probe{{Name: "jobs_workflow", Read: func(ctx context.Context) (healthlayer.Sample, error) { return runtimehealth.Read(ctx, m) }}})
		r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "deployment-health", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
		registerHealthTools(r, c)
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"health_snapshot"}})
		value, err := r.readers["health_snapshot"](ctx, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		report := value.(healthlayer.Report)
		var data struct {
			Deployments struct {
				State string `json:"state"`
			} `json:"deployments"`
		}
		if err := json.Unmarshal(report.Items[0].Data, &data); err != nil {
			t.Fatal(err)
		}
		want := "ok"
		if corrupt {
			want = "unknown"
		}
		if data.Deployments.State != want || report.State != want {
			t.Fatalf("corrupt=%v: %+v / %+v", corrupt, report, data)
		}
	}
}
