package statestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthorityMarkerFailsClosedAndUpsert(t *testing.T) {
	root := t.TempDir()
	if db, err := Active(root); err != nil || db != nil {
		t.Fatal("legacy selection failed")
	}
	os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600)
	if _, err := Active(root); err == nil {
		t.Fatal("missing database fell back")
	}
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put("jobs/a/metadata.json", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put("jobs/a/metadata.json", []byte("two")); err != nil {
		t.Fatal(err)
	}
	db, err = Active(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Get("jobs/a/metadata.json")
	if err != nil || string(got) != "two" {
		t.Fatal("lost durable update", err)
	}
}
