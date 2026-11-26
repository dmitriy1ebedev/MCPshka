package jobs

import (
	"strings"
	"testing"
	"time"
)

// A negative cursor reads the tail (RDC read_process_output offset -N).
func TestReadNegativeCursorIsTheTail(t *testing.T) {
	m, work := testManager(t)
	j, err := m.Start("printf", []string{"%s", strings.Repeat("a", 100) + "END"}, work)
	if err != nil {
		t.Fatal(err)
	}
	if ch := m.Changed(j.ID); ch != nil {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("job did not end")
		}
	}
	out, err := m.Read(j.ID, "stdout", -3, 0)
	if err != nil || out.Text != "END" || out.Cursor != 100 || out.NextCursor != 103 || out.Truncated {
		t.Fatalf("tail: %+v %v", out, err)
	}
	out, err = m.Read(j.ID, "stdout", -10, 4)
	if err != nil || out.Text != "aaaa" || out.Cursor != 93 {
		t.Fatalf("tail with a smaller limit starts at the tail: %+v %v", out, err)
	}
	if out, err = m.Read(j.ID, "stdout", -1000, 0); err != nil || len(out.Text) != 103 {
		t.Fatalf("a tail longer than the output reads all of it: %+v %v", out, err)
	}
}
