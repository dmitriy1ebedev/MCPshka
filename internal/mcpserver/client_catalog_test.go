package mcpserver

import (
	"testing"
)

func TestToolsListIsRecordedForTheConnectedClient(t *testing.T) {
	c := catalogClient(t, nil)
	tools := catalogTools(t, c)
	st := catalogCall(t, c, "client_catalog_status", map[string]any{})
	clients, _ := st["clients"].([]any)
	if len(clients) != 1 {
		t.Fatal("tools/list not recorded", st)
	}
	got := clients[0].(map[string]any)
	if int(got["tools_seen"].(float64)) != len(tools) || got["stale"] != false {
		t.Fatal(got)
	}
	boot := catalogCall(t, c, "capabilities_bootstrap", map[string]any{})
	if boot["client_snapshot"] != nil {
		t.Fatal("complete client told it is stale", boot["client_snapshot"])
	}
}
