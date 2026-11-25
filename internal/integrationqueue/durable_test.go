package integrationqueue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableRoundtripAndFencedRecovery(t *testing.T) {
	q := new(Queue)
	commit := strings.Repeat("a", 40)
	if err := q.Enqueue("task", "feature/p19", commit); err != nil {
		t.Fatal(err)
	}
	if err := q.Validate("task", commit); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := q.Save(path); err != nil {
		t.Fatal(err)
	}
	restored := new(Queue)
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	items := restored.Snapshot()
	if len(items) != 1 || items[0].State != Failed {
		t.Fatalf("unsafe recovery: %+v", items)
	}
	if _, err := restored.Claim(); err == nil {
		t.Fatal("reclaimed uncertain integration")
	}
}
func TestDurableTamperRejected(t *testing.T) {
	q := new(Queue)
	if err := q.Enqueue("task", "feature/p1", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := q.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "feature/p1", "feature/p2", 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := new(Queue).Load(path); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}
func TestLoadRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"items":[],"checksum":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := new(Queue).Load(path); err == nil {
		t.Fatal("unknown version accepted")
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	q := new(Queue)
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := q.Save(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\n{}"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := new(Queue).Load(path); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
