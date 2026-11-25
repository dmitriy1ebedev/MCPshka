package ownerweb

import (
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/nodes"
)

// MCP-147: the Connections page lists nodes from the key directory alone
// (no network on page load) and marks an unpinned host key.
func TestNodeStatusesFromKeyDirectory(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldOOB := nodes.Dir, nodes.OOBConfig
	nodes.Dir, nodes.OOBConfig = dir, filepath.Join(dir, "none.json")
	t.Cleanup(func() { nodes.Dir, nodes.OOBConfig = oldDir, oldOOB })
	for name, body := range map[string]string{
		"stand-2":      "PRIVATE",
		"stand-2.pub":  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA mcpshka-stand-2",
		"stand-2.json": `{"host":"203.0.113.9","port":2222,"user":"build"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := nodeStatuses()
	if len(list) != 2 || list[0].Kind != "node-add" {
		t.Fatalf("statuses %+v", list)
	}
	n := list[1]
	if n.ID != "node-stand-2" || n.State != "pin" || n.Node == nil || n.Why != "build@203.0.113.9 порт 2222" || n.Node.PublicKey != "restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA mcpshka-stand-2" {
		t.Fatalf("node %+v %+v", n, n.Node)
	}
}
