package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/healthlayer"
	"os"
	"testing"
)

func TestHealthNativeReadOnlyAndCachedCompatibility(t *testing.T) {
	m, _, in := yieldFixture(t)
	calls := 0
	collector := healthlayer.New([]healthlayer.Probe{{Name: "search_index", Read: func(context.Context) (healthlayer.Sample, error) {
		calls++
		return healthlayer.Sample{State: "unknown"}, nil
	}}})
	srv := mcp.NewServer(&mcp.Implementation{Name: "health-test", Version: "1"}, nil)
	r := &toolRegistry{server: srv, tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerHealthTools(r, collector)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"health_snapshot"}})
	value, e := r.readers["health_snapshot"](ctx, map[string]any{})
	if e != nil || !r.tools["health_snapshot"] {
		t.Fatalf("native read failed: %v", e)
	}
	out := value.(healthlayer.Report)
	if out.Items[0].State != "unknown" {
		t.Fatal("missing index looked healthy")
	}
	if _, e := m.Database().Get("searchindex/v1/health"); !os.IsNotExist(e) {
		t.Fatal("native read wrote index health")
	}
	snap, e := healthSnapshotCompat(context.Background(), m, collector, ExecStartInput{Args: []string{"health-snapshot"}, Workdir: in.Path, IdempotencyKey: "health-read"})
	if e != nil || len(snap.Args) != 1 || calls != 1 {
		t.Fatalf("duplicate or executable probe: %+v %v calls=%d", snap, e, calls)
	}
	data, e := m.Read(snap.ID, "stdout", 0, 16384)
	if e != nil {
		t.Fatal(e)
	}
	var cached healthlayer.Report
	if e = json.Unmarshal([]byte(data.Text), &cached); e != nil || !cached.Cached {
		t.Fatal("compat did not share native cache")
	}
	_, e = healthSnapshotCompat(context.Background(), m, collector, ExecStartInput{Args: []string{"health-snapshot"}, Workdir: in.Path, IdempotencyKey: "override", Env: map[string]string{"EXTRA": "1"}})
	if e == nil {
		t.Fatal("accepted overrides")
	}
}
