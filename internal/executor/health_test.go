package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthPermitAgeDoesNotFence(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	if err = c.Create(p); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r, err := c.Health(context.Background(), now)
	if err != nil || r.State != "ok" {
		t.Fatalf("new permit: %+v %v", r, err)
	}
	raw, _ := db.Get(key(p))
	var stored map[string]any
	json.Unmarshal(raw, &stored)
	stored["created_at"] = now.Add(-2 * time.Hour)
	before, _ := db.Get(key(p))
	raw, _ = json.Marshal(stored)
	db.Replace(key(p), before, raw)
	r, err = c.Health(context.Background(), now)
	if err != nil || r.State != "degraded" || r.Overdue != 1 {
		t.Fatalf("old permit: %+v %v", r, err)
	}
	s, err := c.Inspect(context.Background(), p)
	if err != nil || s.State != "ACTIVE" {
		t.Fatal("health changed authority", s, err)
	}
	delete(stored, "created_at")
	before, _ = db.Get(key(p))
	raw, _ = json.Marshal(stored)
	db.Replace(key(p), before, raw)
	r, err = c.Health(context.Background(), now)
	if err != nil || r.State != "unknown" {
		t.Fatal("legacy age guessed", r, err)
	}
}

func TestHealthBoundedPermitScanNeverInventsOK(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	for i := 0; i < maxHealthPermits+1; i++ {
		p := Pin{PermitID: fmt.Sprintf("%032x", i+1), LeaseID: fmt.Sprintf("%032x", i+100), ExecutionID: fmt.Sprintf("%032x", i+200), Generation: 1}
		if err := c.Create(p); err != nil {
			t.Fatal(err)
		}
	}
	r, err := c.Health(context.Background(), time.Now().UTC())
	if err != nil || !r.Truncated || r.State != "unknown" || r.Checked != maxHealthPermits {
		t.Fatalf("unbounded or false green: %+v %v", r, err)
	}
}

func TestHealthScansManyPermitsWithinBrokerBudget(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	for i := 0; i < 128; i++ {
		p := Pin{PermitID: fmt.Sprintf("%032x", i+1), LeaseID: fmt.Sprintf("%032x", i+1000), ExecutionID: fmt.Sprintf("%032x", i+2000), Generation: 1}
		if err := c.Create(p); err != nil {
			t.Fatal(err)
		}
	}
	// The broker grants 4 s; a sqlite3 start per record missed it at load 12.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r, err := c.Health(ctx, time.Now().UTC())
	if err != nil || r.Checked != 128 || r.Pending != 128 || r.Truncated || r.State != "ok" {
		t.Fatalf("scan: %+v %v", r, err)
	}
}
