package jobs

import (
	"encoding/json"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteJobRecoveryUsesSoleAuthority(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{ID: "persisted", Command: "go", Args: []string{"version"}, State: Running}
	data, _ := json.Marshal(s)
	if err = db.Put("jobs/persisted/metadata.json", data); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600)
	m, err := New(filepath.Join(root, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get("persisted")
	if err != nil || got.State != Unknown {
		t.Fatalf("recovery state=%s err=%v", got.State, err)
	}
	if _, err = os.Stat(filepath.Join(root, "jobs/persisted/metadata.json")); !os.IsNotExist(err) {
		t.Fatal("authoritative JSON mirror")
	}
	m2, err := New(filepath.Join(root, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	got, err = m2.Get("persisted")
	if err != nil || got.State != Unknown {
		t.Fatal("restart durability", err)
	}
}

func TestSQLiteReapRemovesDurableMetadata(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600)
	m, err := New(filepath.Join(root, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{ID: "terminal", Command: "go", State: Done, FinishedAt: time.Now().Add(-time.Hour)}
	if err := m.persist(s); err != nil {
		t.Fatal(err)
	}
	m, err = New(filepath.Join(root, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	if count, err := m.Reap(time.Minute, time.Now()); err != nil || count != 1 {
		t.Fatal("reap", count, err)
	}
	if keys, err := db.List("jobs/"); err != nil || len(keys) != 0 {
		t.Fatal("metadata remained", err)
	}
}
