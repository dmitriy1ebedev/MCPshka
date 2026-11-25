package releasequeue

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/authn"
	"mcpshka/internal/statestore"
)

// TestReleaseQueueDrivesWholeConveyor walks one release from the queue to a
// verified install through fake tools, one step per tick, as the queuing owner.
// A commit shipped by hand (GitHub CI while GitVerse had no runner, 18.11)
// is continued under its own item and evidence, not shipped again.
func TestQueueContinuesManualShip(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	owner := authn.Identity{ID: "chat-owner", Source: "oauth", Role: "operator"}
	commit := strings.Repeat("a", 40)
	in := Input{Version: "0.16.5", Branch: "agent/x", Commit: commit, StoreID: strings.Repeat("b", 32), ShipItem: "ship-" + commit[:12] + "-gh", Evidence: "ci"}
	for _, bad := range []Input{{ShipItem: "ship-" + strings.Repeat("c", 12)}, {ShipItem: "ship-" + commit[:12] + "-../x"}, {Evidence: "magic"}} {
		b := in
		if bad.ShipItem != "" {
			b.ShipItem = bad.ShipItem
		}
		if bad.Evidence != "" {
			b.Evidence = bad.Evidence
		}
		if _, err := Mutate(db, owner, b, time.Now()); err == nil {
			t.Fatalf("refused input accepted: %+v", bad)
		}
	}
	if _, err := Mutate(db, owner, in, time.Now()); err != nil {
		t.Fatal(err)
	}
	var shipArgs map[string]any
	w := &Watcher{DB: db, Now: time.Now, Installed: func(context.Context) string { return "0.16.4" }}
	w.Call = func(_ context.Context, name string, args map[string]any) (map[string]any, error) {
		switch name {
		case "admin_session_status":
			return map[string]any{"state": "active", "id": "s"}, nil
		case "integration_ship":
			shipArgs = args
			return map[string]any{"stage": "PUBLISHED", "permit_id": "p"}, nil
		}
		return map[string]any{}, nil
	}
	items, _ := Load(context.Background(), db)
	it := items[0]
	w.step(context.Background(), &it)
	w.step(context.Background(), &it)
	if shipArgs["id"] != in.ShipItem || shipArgs["evidence"] != "ci" || it.Stage != "BUILD" || it.TestPermit != "p" {
		t.Fatalf("manual ship not continued: %v stage=%s permit=%s", shipArgs, it.Stage, it.TestPermit)
	}
}

func TestReleaseQueueDrivesWholeConveyor(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	owner := authn.Identity{ID: "chat-owner", Source: "oauth", Role: "operator"}
	commit := strings.Repeat("a", 40)
	if _, err := Mutate(db, owner, Input{Version: "0.11.2", Branch: "feat/0.11.2", Commit: commit, StoreID: strings.Repeat("b", 32)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Mutate(db, owner, Input{Version: "0.11.2", Branch: "feat/0.11.2", Commit: strings.Repeat("c", 40), StoreID: strings.Repeat("b", 32)}, time.Now()); err == nil {
		t.Fatal("a second commit for the same version must be refused")
	}
	shipCalls, polls, applied, installed, faceID := 0, 0, 0, false, false
	var calls []string
	w := &Watcher{DB: db, Now: time.Now, Installed: func(context.Context) string { return "0.11.1" }}
	w.Tidy = func(_ context.Context, branch string) []string { return []string{"worktree of " + branch + " removed"} }
	w.Call = func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if id, _ := authn.IdentityFromContext(ctx); id.ID != owner.ID {
			t.Fatalf("%s called as %q", name, id.ID)
		}
		calls = append(calls, name)
		switch name {
		case "admin_session_status":
			if !faceID {
				return map[string]any{"state": "expired"}, nil
			}
			return map[string]any{"id": strings.Repeat("d", 32), "state": "active"}, nil
		case "integration_ship":
			shipCalls++
			switch {
			case args["approval_id"] == nil && shipCalls < 3:
				return map[string]any{"stage": "CI_RUNNING"}, nil
			case args["approval_id"] == nil:
				return map[string]any{"stage": "READY_FOR_APPROVAL", "permit_id": "test-permit"}, nil
			}
			return map[string]any{"stage": "PUBLISHED", "done": true}, nil
		case "integration_approve":
			if args["permit_id"] != "test-permit" {
				t.Fatalf("approve %v", args)
			}
			return map[string]any{"id": strings.Repeat("e", 64)}, nil
		case "release_build_prepare":
			return map[string]any{"state": "SEALED", "permit_id": "build-permit", "launch_id": "launch", "manifest": map[string]any{"source": map[string]any{"release_digest": "digest"}}}, nil
		case "release_local":
			if args["test_permit"] != "test-permit" || args["build_permit"] != "build-permit" {
				t.Fatalf("local %v", args)
			}
			polls++
			if polls == 1 {
				return map[string]any{"stage": "COLLECTING"}, nil
			}
			return map[string]any{"stage": "STAGED"}, nil
		case "phase_gate":
			return map[string]any{"head": commit, "verdict": "PASS", "checks": []any{
				map[string]any{"name": "host_pressure", "status": "PASS"},
			}}, nil
		case "release_apply_status":
			if installed {
				return map[string]any{"state": "INSTALLED"}, nil
			}
			if applied > 0 {
				installed = true
				return map[string]any{"state": "APPLYING"}, nil
			}
			return map[string]any{"state": "NOT_STARTED", "installed_version": "0.11.1"}, nil
		case "release_apply":
			applied++
			return map[string]any{"state": "APPLYING"}, nil
		case "project_verify":
			return map[string]any{"ok": true, "rollback_version": "0.11.1"}, nil
		}
		t.Fatalf("unexpected tool %s", name)
		return nil, nil
	}
	stage := func() Item {
		items, err := Load(context.Background(), db)
		if err != nil || len(items) != 1 {
			t.Fatalf("%v %v", items, err)
		}
		return items[0]
	}
	w.Tick(context.Background())
	if it := stage(); it.Stage != "QUEUED" || !strings.Contains(it.Detail, "Face ID") {
		t.Fatalf("without Face ID the release must wait: %+v", it)
	}
	faceID = true
	for i := 0; i < 20 && stage().Stage != "DONE"; i++ {
		w.Tick(context.Background())
	}
	it := stage()
	if it.Stage != "DONE" || polls != 2 || applied != 1 || it.ApprovalID == "" {
		t.Fatalf("release did not finish once: %+v applied=%d calls=%v", it, applied, calls)
	}
	if last := it.History[len(it.History)-1]; !strings.Contains(last, "CLEANUP worktree of feat/0.11.2 removed") {
		t.Fatalf("no cleanup after DONE: %v", it.History)
	}
	raw, _ := json.Marshal(it.view())
	if strings.Contains(string(raw), "oauth") {
		t.Fatalf("view leaks identity details: %s", raw)
	}
	// A finished release is never driven again.
	n := len(calls)
	w.Tick(context.Background())
	if len(calls) != n {
		t.Fatal("terminal release was stepped again")
	}
}

func TestReleaseQueueHoldsWithoutExactGateAndOnlyObservesInFlight(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tt := range []struct {
		name, state, verdict, head, pressure, installed string
		want                                            string
	}{
		{"blocked", "NOT_STARTED", "BLOCKED", commit, "PASS", "0.17.1", "phase_gate"},
		{"other commit", "NOT_STARTED", "PASS", strings.Repeat("b", 40), "PASS", "0.17.1", "phase_gate"},
		{"pressure", "NOT_STARTED", "PASS", commit, "WARN", "0.17.1", "host_pressure"},
		{"drift", "NOT_STARTED", "PASS", commit, "PASS", "0.17.0", "rollback checkpoint"},
		{"unknown", "HOLD", "PASS", commit, "PASS", "0.17.1", "HOLD"},
		{"running", "APPLYING", "PASS", commit, "PASS", "0.17.1", "installing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := Item{Stage: "APPLY", Version: "0.17.2", PreviousVersion: "0.17.1", Commit: commit}
			w := &Watcher{Now: time.Now, Call: func(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
				switch name {
				case "release_apply_status":
					return map[string]any{"state": tt.state, "installed_version": tt.installed}, nil
				case "phase_gate":
					return map[string]any{"verdict": tt.verdict, "head": tt.head, "checks": []any{
						map[string]any{"name": "host_pressure", "status": tt.pressure},
					}}, nil
				default:
					t.Fatalf("unexpected tool %s", name)
					return nil, nil
				}
			}}
			w.step(context.Background(), &item)
			if item.Stage != "APPLY" || !strings.Contains(item.Detail, tt.want) {
				t.Fatalf("unexpected release state: %s %s", item.Stage, item.Detail)
			}
		})
	}
}

func TestReleaseQueueStopsOnFailedTestAndCancel(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	owner := authn.Identity{ID: "chat-owner"}
	in := Input{Version: "0.11.3", Branch: "feat/x", Commit: strings.Repeat("a", 40), StoreID: strings.Repeat("b", 32)}
	if _, err := Mutate(db, owner, in, time.Now()); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{DB: db, Now: time.Now, Installed: func(context.Context) string { return "" }, Call: func(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
		if name == "admin_session_status" {
			return map[string]any{"id": strings.Repeat("d", 32), "state": "active"}, nil
		}
		return map[string]any{"stage": "TEST_FAILED", "detail": "sealed test FAILED"}, nil
	}}
	w.Tick(context.Background())
	w.Tick(context.Background())
	items, _ := Load(context.Background(), db)
	if items[0].Stage != "FAILED" {
		t.Fatalf("failed test must stop the release: %+v", items[0])
	}
	in.Version, in.Commit = "0.11.4", strings.Repeat("f", 40)
	if _, err := Mutate(db, owner, in, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v, err := Mutate(db, owner, Input{Action: "cancel", Version: "0.11.4"}, time.Now()); err != nil || v.Stage != "CANCELLED" {
		t.Fatalf("cancel: %+v %v", v, err)
	}
}
