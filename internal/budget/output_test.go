package budget

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputNeverRetainsLargeWrite(t *testing.T) {
	for _, limit := range []int{1, 8192, 64 << 10} {
		o := Output{Limit: limit}
		input := bytes.Repeat([]byte("✅"), 1<<20)
		if n, err := o.Write(input); err != nil || n != len(input) {
			t.Fatal(n, err)
		}
		if len(o.head)+len(o.tail) > max(0, limit-1024) || cap(o.tail) > 2*limit {
			t.Fatal("unbounded retained allocation", len(o.tail), cap(o.tail))
		}
		if text := o.String(); len(text) > limit || !utf8.ValidString(text) {
			t.Fatal("wire bound/encoding", len(text))
		}
	}
}
func TestOutputShortAndBinaryWrites(t *testing.T) {
	var o Output
	o.Write([]byte("first\n"))
	o.Write([]byte("last\n"))
	if o.String() != "first\nlast\n" {
		t.Fatal(o.String())
	}
	for range 20000 {
		o.Write([]byte{255, 0xE2, 0x9C, 0x85})
	}
	text := o.String()
	if len(text) > 8192 || !utf8.ValidString(text) || !strings.HasPrefix(text, "first\n") || !strings.Contains(text, "bytes omitted") {
		t.Fatal("stream truncation or encoding", len(text))
	}
}

func TestOutputKeepsFailuresFromTheOmittedMiddle(t *testing.T) {
	o := &Output{Limit: 4096}
	o.Write([]byte(strings.Repeat("ok  \tmcpshka/internal/a\t0.1s\n", 100)))
	o.Write([]byte("--- FAIL: TestToolForgeAudit (0.02s)\n    forge_test.go:88: audit 131 KiB > 128 KiB\nFAIL\tmcpshka/internal/mcp"))
	o.Write([]byte("server\t9.1s\n"))
	o.Write([]byte(strings.Repeat("ok  \tmcpshka/internal/z\t0.1s\n", 200)))
	s := o.String()
	if len(s) > 4096 || !strings.Contains(s, "failures inside the omitted part:\n--- FAIL: TestToolForgeAudit (0.02s)\nFAIL\tmcpshka/internal/mcpserver\t9.1s") {
		t.Fatalf("%d bytes:\n%s", len(s), s)
	}
}
