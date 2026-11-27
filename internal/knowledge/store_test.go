package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func record(id string) Record {
	return Record{ID: id, Kind: "device", Source: "diag", Scope: "h96", Provenance: "probe", Timestamp: time.Now().UTC(), ExportMetadata: true, Fields: map[string]Field{"public": {Value: "mqtt ready", Export: true}, "secret": {Value: "token", Secret: true, Export: true}}}
}
func TestPersistenceExportForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	r := record("a")
	if e = s.Add(r); e != nil {
		t.Fatal(e)
	}
	child := record("b")
	child.Provenance = "a"
	if e = s.Add(child); e != nil {
		t.Fatal(e)
	}
	b, e := s.Export()
	if e != nil || strings.Contains(string(b), "token") {
		t.Fatalf("leaked secret %s %v", b, e)
	}
	q, e := s.Search(context.Background(), Query{Text: "mqtt", Limit: 10})
	if e != nil || len(q) != 2 {
		t.Fatalf("%v %v", q, e)
	}
	for _, result := range q {
		if _, leaked := result.Fields["secret"]; leaked {
			t.Fatalf("search exposed secret field in %s", result.ID)
		}
	}
	stored, e := s.Inspect("a")
	if e != nil || stored.Fields["secret"].Value != "token" {
		t.Fatalf("search mutated stored record: %v", e)
	}
	if e = s.Forget("a"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Inspect("b"); e != ErrMissing {
		t.Fatal(e)
	}
	reopened, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.Inspect("a"); e != ErrMissing {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v %v", info, e)
	}
}
func TestProvenanceAndAtomicImport(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.json"))
	r := record("bad")
	r.Provenance = ""
	if e := s.Add(r); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e := s.Import([]byte("not json")); e == nil {
		t.Fatal("accepted corrupt import")
	}
	if _, e := s.Inspect("bad"); e != ErrMissing {
		t.Fatal(e)
	}
	r = record("a")
	if e := s.Add(r); e != nil {
		t.Fatal(e)
	}
	if e := s.Add(r); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	replacement := record("new")
	if e := s.Supersede("a", replacement); e != nil {
		t.Fatal(e)
	}
	q, e := s.Search(context.Background(), Query{Limit: 10})
	if e != nil || len(q) != 1 || q[0].ID != "new" {
		t.Fatalf("%v %v", q, e)
	}
}

func TestExportLessonsBackfill(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "store.json"))
	if e != nil {
		t.Fatal(e)
	}
	lesson := Record{ID: "lesson-mcp-001", Kind: "lesson", Source: "issues/MCP-001", Scope: "mcpshka", Provenance: "issue_accept x", Timestamp: time.Now().UTC(), Fields: map[string]Field{"title": {Value: "MCP-001 · leases", Export: true}}}
	private := lesson
	private.ID, private.Source = "lesson-mcp-002", "issues/MCP-002"
	private.Fields = map[string]Field{"title": {Value: "hidden", Export: false}}
	for _, r := range []Record{lesson, private} {
		if e = s.Add(r); e != nil {
			t.Fatal(e)
		}
	}
	if n, e := s.ExportLessons(); e != nil || n != 1 {
		t.Fatal("backfill", n, e)
	}
	if n, e := s.ExportLessons(); e != nil || n != 0 {
		t.Fatal("second backfill must write nothing", n, e)
	}
	q, e := s.Search(context.Background(), Query{Text: "leases", Limit: 10})
	if e != nil || len(q) != 1 || q[0].ID != "lesson-mcp-001" {
		t.Fatal("lesson not searchable", q, e)
	}
}
