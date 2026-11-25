package hostevents

import (
	"mcpshka/internal/hostdiag"
	"strings"
	"testing"
)

func TestFilterBoundsAndCounts(t *testing.T) {
	var c hostdiag.Counts
	lines, truncated := Filter(strings.Repeat("killed process "+strings.Repeat("x", 320)+"\n", 45), false, &c)
	if !truncated || len(lines) != 40 || c.OOMKill != 45 || len(lines[0]) != 300 {
		t.Fatalf("unbounded or lost counts: %d %+v %v", len(lines), c, truncated)
	}
	lines, truncated = Filter("ordinary log\nbroken pipe\nfork failed\n", true, &c)
	if truncated || len(lines) != 2 || c.SSHDrop != 1 || c.ForkFailed != 1 {
		t.Fatalf("SSH projection: %v %+v", lines, c)
	}
}
