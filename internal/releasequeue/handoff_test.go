package releasequeue

import (
	"context"
	"mcpshka/internal/agents"
	"mcpshka/internal/handoff"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatcherProviderEvidenceEnablesOnlyFreshAuthenticatedHandoff(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := handoff.Packet{Objective: "test", Scope: "test", HEAD: strings.Repeat("a", 40), Owner: "owner", RunID: "run", Checkpoint: "checkpoint", NextSafeAction: "reconcile", Stage: "UNKNOWN"}
	if err = handoff.Save(db, "packet", p); err != nil {
		t.Fatal(err)
	}
	if err = handoff.SetPolicy(db, handoff.Policy{Provider: "codex", Tier: "untrusted"}); err != nil {
		t.Fatal(err)
	}
	if _, err = handoff.Export(db, "packet", "codex", now); err == nil {
		t.Fatal("missing authentication exported")
	}
	auth, observed := "ok", now
	w := Watcher{DB: db, Now: func() time.Time { return now }, ProviderAuth: func(context.Context, time.Time) []agents.ProviderHealth {
		return []agents.ProviderHealth{{Name: "codex", Authorization: agents.HealthSignal{State: auth, ObservedAt: &observed}}}
	}}
	w.Tick(context.Background())
	if _, err = handoff.Export(db, "packet", "codex", now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	observed = now
	auth = "degraded"
	w.Tick(context.Background())
	if _, err = handoff.Export(db, "packet", "codex", now); err == nil {
		t.Fatal("observed authentication failure did not revoke export")
	}
	observed = now.Add(-time.Second)
	auth = "ok"
	w.Tick(context.Background())
	if _, err = handoff.Export(db, "packet", "codex", now); err == nil {
		t.Fatal("old task undid provider revocation")
	}
	if _, err = handoff.Export(db, "packet", "codex", now.Add(6*time.Minute)); err == nil {
		t.Fatal("stale authentication exported")
	}
}
