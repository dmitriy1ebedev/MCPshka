package rootbroker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MCP-115: the live file is capped and status reads only its tail.
func TestApprovedLiveOutput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "live.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	c := &cappedFile{f: f, left: 3000}
	if n, err := c.Write([]byte(strings.Repeat("a", 2500) + "STEP2")); err != nil || n != 2505 {
		t.Fatalf("write %d %v", n, err)
	}
	if n, _ := c.Write([]byte(strings.Repeat("b", 1000))); n != 1000 {
		t.Fatal("capped write must still report the full length")
	}
	f.Close()
	if st, _ := os.Stat(p); st.Size() != 3000 {
		t.Fatalf("size %d", st.Size())
	}
	got := fileTail(p, 2048)
	if len(got) != 2048 || !strings.Contains(got, "STEP2") {
		t.Fatalf("tail %d %q", len(got), got[len(got)-20:])
	}
	if fileTail(filepath.Join(t.TempDir(), "none"), 2048) != "" {
		t.Fatal("missing file must give empty tail")
	}
}
