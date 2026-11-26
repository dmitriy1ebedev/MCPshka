package roadmapnext

import (
	"strings"
	"testing"
)

// MCP-052: closing a phase in a large ROADMAP stays linear.
func BenchmarkMarkDone(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 400; i++ {
		sb.WriteString("## Phase " + strings.Repeat("x", i%7) + "\n- [ ] step\n- [x] done\n")
	}
	sb.WriteString("## Target\n- [ ] a\n- [-] b\n")
	roadmap := sb.String()
	b.ReportAllocs()
	for b.Loop() {
		if _, n, err := MarkDone(roadmap, Item{Title: "Target"}); err != nil || n != 2 {
			b.Fatal(n, err)
		}
	}
}
