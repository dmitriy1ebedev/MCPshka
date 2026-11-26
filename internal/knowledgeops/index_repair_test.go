package knowledgeops

import (
	"context"
	"encoding/json"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/policy"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexHealthRepairRollbackPreservesConcurrentWriter(t *testing.T) {
	state := t.TempDir()
	db, err := statestore.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := jobs.New(filepath.Join(state, "jobs"), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	store, err := knowledge.FromDatabaseContext(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(IndexHealth{SourceRevision: "stale"})
	if err = db.Put(indexHealthKey, before); err != nil {
		t.Fatal(err)
	}
	b := &indexHealthBackend{db: db, store: store, before: before, revision: revision}
	ctx := context.Background()
	target := selfheal.Target{}
	receipt, err := b.Repair(ctx, target, "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := b.Verify(ctx, target, receipt); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := b.Rollback(ctx, target, "wrong-receipt"); err == nil || ok {
		t.Fatal("mismatched receipt authorized rollback", ok, err)
	}
	if ok, err := b.Rollback(ctx, target, receipt); err != nil || !ok {
		t.Fatal(ok, err)
	}
	raw, _ := db.Get(indexHealthKey)
	if string(raw) != string(before) {
		t.Fatal("rollback did not restore original receipt")
	}
	if receipt, err = b.Repair(ctx, target, ""); err != nil {
		t.Fatal(err)
	}
	newer := []byte("new foreground receipt")
	if err = db.Put(indexHealthKey, newer); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.Rollback(ctx, target, receipt); err != nil || ok {
		t.Fatal("rollback replaced a newer writer", ok, err)
	}
	raw, _ = db.Get(indexHealthKey)
	if string(raw) != string(newer) {
		t.Fatal("new writer lost")
	}
}
