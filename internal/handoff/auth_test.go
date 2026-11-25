package handoff

import (
	"encoding/json"
	"mcpshka/internal/statestore"
	"path/filepath"
	"testing"
	"time"
)

func TestHandoffAuthenticationMonotonicAndRevocationWinsTies(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, step := range []struct {
		ok bool
		at time.Time
	}{{true, now}, {false, now}, {true, now}, {true, now.Add(-time.Minute)}} {
		if err = ObserveAuthentication(db, "codex", step.ok, step.at); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := db.Get("handoff/auth/codex")
	if err != nil {
		t.Fatal(err)
	}
	var a authObservation
	if err = json.Unmarshal(raw, &a); err != nil || a.Authenticated || !a.ObservedAt.Equal(now) {
		t.Fatal(a, err)
	}
	if err = ObserveAuthentication(db, "codex", true, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, _ = db.Get("handoff/auth/codex")
	if err = json.Unmarshal(raw, &a); err != nil || !a.Authenticated {
		t.Fatal(a, err)
	}
	if ObserveAuthentication(db, "codex", true, time.Time{}) == nil {
		t.Fatal("zero timestamp accepted")
	}
}
