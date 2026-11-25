package idempotency

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcpshka/internal/statestore"
)

func TestSQLiteIdempotencySurvivesRestartWithoutJSONMirror(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "idempotency")
	s := NewWithDatabase(dir, db)
	first, err := s.Begin("request", "action", "fingerprint")
	if err != nil || !first.Fresh {
		t.Fatalf("begin err=%v fresh=%v", err, first.Fresh)
	}
	if err := s.Complete("request", "action", "fingerprint", map[string]string{"result": "verified"}); err != nil {
		t.Fatal(err)
	}
	s = NewWithDatabase(dir, db)
	second, err := s.Begin("request", "action", "fingerprint")
	if err != nil || second.Fresh || second.Entry.State != Completed {
		t.Fatalf("replay err=%v fresh=%v", err, second.Fresh)
	}
	var result map[string]string
	if state, err := s.ReadResult("request", "action", "fingerprint", &result); err != nil || state != Completed || result["result"] != "verified" {
		t.Fatalf("recovered state=%s err=%v", state, err)
	}
	if _, err := os.Stat(s.pathForKey("request")); !os.IsNotExist(err) {
		t.Fatal("SQL-authoritative store created a JSON mirror")
	}
	if _, err := s.Begin("request", "other", "fingerprint"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestSQLiteDoesNotFallBackToLegacyState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "idempotency")
	legacy := New(dir)
	if _, err := legacy.Begin("request", "action", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithDatabase(dir, db)
	if _, err := s.Lookup("request"); err == nil {
		t.Fatal("legacy JSON became a second authority")
	}
	data, err := os.ReadFile(legacy.pathForKey("request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutIfAbsent("idempotency/"+filepath.Base(legacy.pathForKey("request")), data); err != nil {
		t.Fatal(err)
	}
	if state, err := s.Lookup("request"); err != nil || state.State != Pending {
		t.Fatalf("migrated record err=%v", err)
	}
}
