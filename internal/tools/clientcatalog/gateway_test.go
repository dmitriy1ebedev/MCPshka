package clientcatalog

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestGatewayClientCatalogIgnoresInternalToolGrowth(t *testing.T) {
	coreTools := []string{"discover", "status"}
	c := New(Deps{CoreTools: coreTools})
	raw, _ := json.Marshal(map[string]any{"client": "gateway-client", "surface": "gateway", "tools": coreTools})
	var entry ClientCatalogEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	slices.Sort(entry.Tools)
	current := append(append([]string{}, coreTools...), "internal_new_capability")
	v := c.view(&entry, current)
	if v.MissingCount != 0 || v.Stale {
		t.Fatalf("internal catalog growth requires connector refresh: %+v", v)
	}
}
