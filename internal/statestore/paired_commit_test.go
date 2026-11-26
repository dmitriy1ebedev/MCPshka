package statestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPairedCommitRollsBackLeaseWhenReceiptUpdateFails(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.PutIfAbsent("intent", []byte("pending")); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := db.run("CREATE TRIGGER fail_receipt BEFORE UPDATE ON records WHEN NEW.path='intent' BEGIN SELECT RAISE(ABORT,'injected'); END;"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ReplaceWithJournalUnlessGate("intent", []byte("pending"), []byte("completed"), "lease", []byte("credential"), "writer-gate"); err == nil || ok {
		t.Fatal("fault committed", ok, err)
	}
	if _, err := db.Get("lease"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt failure stranded lease", err)
	}
	if data, err := db.Get("intent"); err != nil || string(data) != "pending" {
		t.Fatal("fault changed intent", err)
	}
}
func TestPairedCommitRejectsGateAliasingItsOwnRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutIfAbsent("intent", []byte("pending")); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"intent", "lease"} {
		if ok, err := db.ReplaceWithJournalUnlessGate("intent", []byte("pending"), []byte("completed"), "lease", []byte("credential"), gate); err == nil || ok {
			t.Fatal("self-gating commit accepted", gate)
		}
	}
	if _, err := db.Get("lease"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid gate wrote lease", err)
	}
}
