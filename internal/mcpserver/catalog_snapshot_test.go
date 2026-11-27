package mcpserver

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestCatalogSnapshot(t *testing.T) {
	tools := catalogTools(t, fullCatalogClient(t))
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/tools_list.snapshot.json"
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		got := path + ".got"
		if err := os.WriteFile(got, raw, 0o644); err == nil {
			t.Fatalf("tools/list changed; actual catalog written to %s (review, then rename over the snapshot)", got)
		}
		t.Fatal("tools/list changed")
	}
}
