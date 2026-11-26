package projectlease

import (
	"encoding/json"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"testing"
)

func TestReleasePinnedWithJournalRejectsStaleIdentityAndManagedOwner(t *testing.T) {
	root := t.TempDir()
	if _, e := statestore.Open(filepath.Join(root, "state.db")); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	s := testService(t, root)
	held, e := s.AcquireTracked(root, "holder", "0123456789abcdef0123456789abcdef")
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"token", "id", "generation", "execution"} {
		token, id, gen, execution := held.Token, held.Status.LeaseID, held.Status.Generation, held.Status.ExecutionID
		switch kind {
		case "token":
			token = "bad"
		case "id":
			id = "bad"
		case "generation":
			gen++
		case "execution":
			execution = "bad"
		}
		if _, e := s.ReleasePinnedWithJournal(root, token, id, gen, execution, "project-yields/v1/"+kind, []byte("proof")); e == nil {
			t.Fatal("stale identity released:", kind)
		}
		if _, e := s.ValidateHolder(root, held.Token); e != nil {
			t.Fatal(e)
		}
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	raw, e := s.database.Get(key)
	if e != nil {
		t.Fatal(e)
	}
	var r record
	if e = json.Unmarshal(raw, &r); e != nil {
		t.Fatal(e)
	}
	r.OwnerJobID = "managed-writer"
	changed, _ := json.Marshal(r)
	if ok, e := s.database.Replace(key, raw, changed); e != nil || !ok {
		t.Fatal(e)
	}
	if _, e := s.ReleasePinnedWithJournal(root, held.Token, held.Status.LeaseID, held.Status.Generation, held.Status.ExecutionID, "project-yields/v1/managed", []byte("proof")); e == nil {
		t.Fatal("managed owner voluntarily released")
	}
	if _, e := s.database.Get(key); e != nil {
		t.Fatal("managed owner removed")
	}
}
