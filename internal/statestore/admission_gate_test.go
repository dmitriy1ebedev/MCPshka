package statestore

import (
	"path/filepath"
	"testing"
)

func TestAdmissionGateRejectsLateInsertAndHeartbeat(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	const gate = "project-write-gates/v1/test"
	if ok, e := db.PutIfAbsentUnlessGate("leases/held", []byte("old"), gate); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := db.PutIfAbsent(gate, []byte("closed")); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := db.PutIfAbsentUnlessGate("leases/late", []byte("new"), gate); e != nil || ok {
		t.Fatal("late claim admitted", ok, e)
	}
	if ok, e := db.ReplaceUnlessGate("leases/held", []byte("old"), []byte("renewed"), gate); e != nil || ok {
		t.Fatal("late heartbeat admitted", ok, e)
	}
	got, e := db.Get("leases/held")
	if e != nil || string(got) != "old" {
		t.Fatal("holder changed", string(got), e)
	}
	if ok, e := db.PutIfAbsentUnlessGate("leases/operator", []byte("trusted"), ""); e != nil || !ok {
		t.Fatal("trusted operator rejected", ok, e)
	}
}
