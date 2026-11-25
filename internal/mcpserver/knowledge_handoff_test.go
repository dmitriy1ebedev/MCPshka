package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/handoff"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestKnowledgeHandoffPacketThroughMCP(t *testing.T) {
	state := t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(state, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	db := manager.Database()
	packet := handoff.Packet{Objective: "accepted goal", Scope: "project scope", HEAD: strings.Repeat("a", 40), Owner: "owner",
		RunID: "run", Checkpoint: "verified checkpoint", NextSafeAction: "reconcile original", Stage: "UNKNOWN",
		Receipts: []string{"job:example"}, Fields: map[string]handoff.Field{
			"secret":   {Value: "NEVER_EXPORT", MinTrust: "untrusted", Secret: true},
			"topology": {Value: "PRIVATE_TOPOLOGY", MinTrust: "local"},
		}}
	if err := handoff.Save(db, "packet", packet); err != nil {
		t.Fatal(err)
	}
	if err := handoff.SetPolicy(db, handoff.Policy{Provider: "mock", Tier: "untrusted"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(manager, nil).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "handoff-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	read := func() *mcp.CallToolResult {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "knowledge_read", Arguments: map[string]any{"action": "handoff", "id": "packet", "provider": "mock"}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := read(); !result.IsError || !strings.Contains(mcpErrorText(result), "HANDOFF_AUTH_UNKNOWN") {
		t.Fatal("unconfirmed provider exported packet")
	}
	if err := handoff.ObserveAuthentication(db, "mock", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{"untrusted", "trusted"} {
		if err := handoff.SetPolicy(db, handoff.Policy{Provider: "mock", Tier: tier}); err != nil {
			t.Fatal(err)
		}
		result := read()
		if result.IsError {
			t.Fatal(mcpErrorText(result))
		}
		raw, err := json.Marshal(result.StructuredContent)
		var out KnowledgeReadResult
		if err != nil || json.Unmarshal(raw, &out) != nil {
			t.Fatal(string(raw), err)
		}
		p := out.Packet
		if out.Trust != "UNTRUSTED" || p["stage"] != "UNKNOWN" || p["head"] != packet.HEAD || p["retry_allowed"] != false || p["dispatch_enabled"] != false {
			t.Fatal(string(raw))
		}
		if strings.Contains(string(raw), "NEVER_EXPORT") || strings.Contains(string(raw), "PRIVATE_TOPOLOGY") {
			t.Fatal("secret or topology exported")
		}
		_, visible := p["objective"]
		if visible != (tier == "trusted") {
			t.Fatal("trust tier lost", string(raw))
		}
		if len(out.Records) != 0 {
			t.Fatal("packet misrepresented as knowledge records")
		}
	}
	if err := handoff.ObserveAuthentication(db, "mock", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if result := read(); !result.IsError {
		t.Fatal("revoked provider exported packet")
	}
}
