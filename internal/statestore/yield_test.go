package statestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteWithJournalAtomicAndConflict(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	old := []byte("pinned lease")
	if _, e = db.PutIfAbsent("leases/test", old); e != nil {
		t.Fatal(e)
	}
	ok, e := db.DeleteWithJournal("leases/test", []byte("wrong lease"), "project-yields/test", []byte("proof"))
	if e != nil || ok {
		t.Fatalf("stale pin changed state: %v %v", ok, e)
	}
	if _, e = db.Get("project-yields/test"); !os.IsNotExist(e) {
		t.Fatalf("failed CAS created proof: %v", e)
	}
	if _, e = db.PutIfAbsent("project-yields/conflict", []byte("different prior journal")); e != nil {
		t.Fatal(e)
	}
	ok, e = db.DeleteWithJournal("leases/test", old, "project-yields/conflict", []byte("proof"))
	if e != nil || ok {
		t.Fatalf("journal collision released holder: %v %v", ok, e)
	}
	if _, e = db.Get("leases/test"); e != nil {
		t.Fatal(e)
	}
	ok, e = db.DeleteWithJournal("leases/test", old, "project-yields/test", []byte("proof"))
	if e != nil || !ok {
		t.Fatalf("atomic release failed: %v %v", ok, e)
	}
	if _, e = db.Get("leases/test"); !os.IsNotExist(e) {
		t.Fatalf("lease survived successful proof: %v", e)
	}
	proof, e := db.Get("project-yields/test")
	if e != nil || string(proof) != "proof" {
		t.Fatalf("release has no durable proof: %s %v", proof, e)
	}
}

func TestReplaceWithJournalAtomicAndImmutable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	old, next, receipt := []byte("legacy-holder"), []byte("new-generation"), []byte("audited handoff")
	if ok, err := db.PutIfAbsent("leases/legacy", old); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := db.ReplaceWithJournal("leases/legacy", []byte("wrong"), next, "lease-recoveries/one", receipt); err != nil || ok {
		t.Fatal("stale CAS admitted", ok, err)
	}
	if _, err := db.Get("lease-recoveries/one"); !os.IsNotExist(err) {
		t.Fatal("journal without CAS", err)
	}
	if ok, err := db.PutIfAbsent("lease-recoveries/used", []byte("different proof")); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := db.ReplaceWithJournal("leases/legacy", old, next, "lease-recoveries/used", receipt); err != nil || ok {
		t.Fatal("existing journal admitted handoff", ok, err)
	}
	if got, err := db.Get("leases/legacy"); err != nil || string(got) != string(old) {
		t.Fatal("lease changed before valid proof", err)
	}
	if ok, err := db.ReplaceWithJournal("leases/legacy", old, next, "lease-recoveries/one", receipt); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got, err := db.Get("leases/legacy"); err != nil || string(got) != string(next) {
		t.Fatal("new holder missing", err)
	}
	if got, err := db.Get("lease-recoveries/one"); err != nil || string(got) != string(receipt) {
		t.Fatal("audit missing", err)
	}
	if ok, err := db.ReplaceWithJournal("leases/legacy", old, []byte("third"), "lease-recoveries/one", receipt); err != nil || ok {
		t.Fatal("replayed transfer admitted", ok, err)
	}
}
