package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
)

func TestColdLeaseRecoveryUsesOriginalAcquireReceiptThenHeartbeat(t *testing.T) {
	manager, leases, in := yieldFixture(t)
	if _, err := leases.Release(in.Path, in.Token); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connect := func(m *jobs.Manager) (*mcp.ClientSession, func()) {
		t.Helper()
		server := New(m, filesystem.New(m.Policy()))
		st, ct := mcp.NewInMemoryTransports()
		ss, err := server.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "cold", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cs, func() { cs.Close(); ss.Close() }
	}
	call := func(cs *mcp.ClientSession, name string, args map[string]any, fail bool) *mcp.CallToolResult {
		t.Helper()
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError != fail {
			t.Fatalf("%s unexpected result (error=%v expected_failure=%v)", name, err, fail)
		}
		return r
	}
	args := map[string]any{"path": in.Path, "owner": "same-owner", "idempotency_key": "original-acquire"}
	cs, disconnect := connect(manager)
	var first projectlease.AcquireResult
	raw, _ := json.Marshal(call(cs, "project_lease_acquire", args, false).StructuredContent)
	if err := json.Unmarshal(raw, &first); err != nil || first.Token == "" {
		t.Fatal("missing original receipt")
	}
	disconnect()
	if err := manager.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	cold, err := jobs.New(manager.StateDir(), manager.Policy())
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close(time.Second)
	cs, disconnect = connect(cold)
	defer disconnect()
	var recovered projectlease.AcquireResult
	raw, _ = json.Marshal(call(cs, "project_lease_acquire", args, false).StructuredContent)
	if err := json.Unmarshal(raw, &recovered); err != nil || recovered.Token != first.Token || recovered.Status.LeaseID != first.Status.LeaseID || recovered.Status.Generation != first.Status.Generation {
		t.Fatal("recovery replaced holder identity")
	}
	pulse := call(cs, "project_lease_heartbeat", map[string]any{"path": in.Path, "token": recovered.Token, "idempotency_key": "recovered-heartbeat"}, false)
	var status projectlease.Status
	raw, _ = json.Marshal(pulse.StructuredContent)
	if err := json.Unmarshal(raw, &status); err != nil || !status.HeartbeatAt.After(first.Status.HeartbeatAt) {
		t.Fatal("recovered holder cannot heartbeat")
	}
	args["idempotency_key"] = "replacement-acquire"
	call(cs, "project_lease_acquire", args, true)
	args["idempotency_key"] = "original-acquire"
	args["owner"] = "foreign-owner"
	call(cs, "project_lease_acquire", args, true)
	call(cs, "project_lease_release", map[string]any{"path": in.Path, "token": recovered.Token, "idempotency_key": "holder-release"}, false)
}
