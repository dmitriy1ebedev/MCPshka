package rootbroker

import (
	"os"
	"testing"
)

func TestStaleLeaseScopeAllowsOnlyGoneTmpClones(t *testing.T) {
	gone := "/tmp/mcpshka-gone-clone-20261010-test"
	if !staleLeaseRepository(gone) {
		t.Fatal("dead /tmp clone lease must be releasable")
	}
	live, err := os.MkdirTemp("/tmp", "mcpshka-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(live)
	for _, p := range []string{live, "/tmp/other", "/tmp/mcpshka-", "/tmp/mcpshka-A", "/tmp/mcpshka-x/../y", "/tmp/mcpshka-x/sub"} {
		if staleLeaseRepository(p) {
			t.Fatalf("%s accepted", p)
		}
	}
}
