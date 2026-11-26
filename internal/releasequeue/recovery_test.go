package releasequeue

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/authn"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseQueueLostApplyReplyColdTickNeverRepeatsInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	item := Item{Version: "0.12.9", Commit: strings.Repeat("a", 40), Stage: "APPLY", Identity: authn.Identity{ID: "owner"}, PreviousVersion: "0.12.8", CreatedAt: now}
	raw, _ := json.Marshal(item)
	if err = db.Put(Prefix+item.Version+".json", raw); err != nil {
		t.Fatal(err)
	}
	applied := 0
	call := func(_ context.Context, name string, args map[string]any) (map[string]any, error) {
		switch name {
		case "admin_session_status":
			return map[string]any{"state": "active", "id": "grant"}, nil
		case "release_apply_status":
			if applied > 0 {
				return map[string]any{"state": "APPLYING"}, nil
			}
			return map[string]any{"state": "NOT_STARTED", "installed_version": "0.12.8"}, nil
		case "phase_gate":
			return map[string]any{"head": item.Commit, "verdict": "PASS", "checks": []any{
				map[string]any{"name": "host_pressure", "status": "PASS"},
			}}, nil
		case "release_apply":
			applied++
			return nil, errors.New("lost reply after accepted install")
		}
		t.Fatal(name)
		return nil, nil
	}
	w := Watcher{DB: db, Now: func() time.Time { return now }, Call: call}
	w.Tick(context.Background())
	pinned, err := Load(context.Background(), db)
	if err != nil || len(pinned) != 1 || pinned[0].ApplySession != "grant" || applied != 0 {
		t.Fatalf("exact grant must be persisted before apply: %v, %v, %d", pinned, err, applied)
	}
	w.Tick(context.Background())
	cold, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := Watcher{DB: cold, Now: func() time.Time { return now.Add(time.Minute) }, Call: call}
	restarted.Tick(context.Background())
	if applied != 1 {
		t.Fatal("install replayed", applied)
	}
	got, err := Load(context.Background(), cold)
	if err != nil || len(got) != 1 || got[0].Stage != "APPLY" || !strings.Contains(got[0].Detail, "installing") {
		t.Fatal(got, err)
	}
}

func TestRootAuditHoldNeverAppliesOrVerifies(t *testing.T) {
	for _, stage := range []string{"APPLY", "VERIFY"} {
		it := Item{Version: "0.13.5", Stage: stage}
		w := Watcher{Now: time.Now, Call: func(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
			switch name {
			case "admin_session_status":
				return map[string]any{"state": "active", "id": "grant"}, nil
			case "release_apply_status":
				return map[string]any{"state": "HOLD", "reason": "root audit ROLLBACK_FAILED"}, nil
			default:
				t.Fatalf("unsafe action during root HOLD: %s", name)
				return nil, nil
			}
		}}
		w.step(context.Background(), &it)
		if it.Stage != stage || !strings.Contains(it.Detail, "HOLD") {
			t.Fatal(it)
		}
	}
}
