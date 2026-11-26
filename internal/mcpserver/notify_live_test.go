package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/notify"
	"mcpshka/internal/releasequeue"
	"testing"
	"time"
)

type portalSender struct {
	calls   int
	outcome string
}

func (s *portalSender) Send(context.Context, notify.Event, string) (string, error) {
	s.calls++
	return s.outcome, nil
}

func TestOwnerPortalChallengeTickAndDelivery(t *testing.T) {
	m, _, _ := yieldFixture(t)
	db := m.Database()
	now := time.Now().UTC()
	if err := notify.TrackAdmin(db, "pending", "owner", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := notify.TrackAdmin(db, "expiring", "owner", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	state := "pending"
	probe := func(context.Context, notify.AdminChallenge) (string, error) { return state, nil }
	w := releasequeue.Watcher{DB: db, Now: func() time.Time { return now }, Challenges: probe}
	w.Tick(context.Background())
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerOwnerPortal(r, nil, db)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"owner_portal"}})
	read := func() []notify.Event {
		t.Helper()
		v, err := r.readers["owner_portal"](ctx, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		return v.(OwnerPortalResult).Events
	}
	events := read()
	n := 0
	for _, e := range events {
		if e.Source == "face_id" {
			n++
			if e.Ref == nil || e.NextSafeAction == "" || e.Delivery == nil || e.Delivery.State != "pending" {
				t.Fatal(e)
			}
		}
		if e.Key == "backup/freshness" && (e.Ref == nil || e.NextSafeAction == "") {
			t.Fatal(e)
		}
	}
	if n != 2 {
		t.Fatal(events)
	}
	sender := &portalSender{outcome: "not_delivered"}
	w.Sender = sender
	w.Tick(ctx)
	first := sender.calls
	w.Tick(ctx)
	if sender.calls != first {
		t.Fatal("backoff not respected")
	}
	now = now.Add(3 * time.Minute)
	sender.outcome = "delivered"
	w.Tick(ctx)
	for _, e := range read() {
		if e.Key == "face-id/pending" && e.Delivery.State != "sent" {
			t.Fatal(e)
		}
	}
	if sender.calls != first+2 {
		t.Fatal("expected backup and pending delivery, expired challenge cancelled", sender.calls, first)
	}
	w.Challenges = func(context.Context, notify.AdminChallenge) (string, error) { return "", errors.New("unknown") }
	w.Tick(ctx)
	found := false
	for _, e := range read() {
		if e.Key == "face-id/pending" {
			found = true
		}
	}
	if !found {
		t.Fatal("unknown probe closed challenge")
	}
	state = "active"
	w.Challenges = probe
	w.Tick(ctx)
	for _, e := range read() {
		if e.Source == "face_id" {
			t.Fatal("confirmed challenge still open", e)
		}
	}
	raw, _ := json.Marshal(read())
	if len(raw) == 0 {
		t.Fatal("missing event response")
	}
}

func TestOwnerPortalReleaseReferences(t *testing.T) {
	m, _, _ := yieldFixture(t)
	if err := notify.Release(m.Database(), "0.12.9", "QUEUED", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerOwnerPortal(r, nil, m.Database())
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"owner_portal"}})
	v, err := r.readers["owner_portal"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	e := v.(OwnerPortalResult).Events
	if len(e) != 1 || e[0].Ref == nil || e[0].NextSafeAction != "release_queue_status" {
		t.Fatal(e)
	}
}
